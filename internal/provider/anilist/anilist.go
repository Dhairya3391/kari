// Package anilist provides a shared AniList GraphQL client used by all anime
// providers. It centralises search and media-detail lookups so individual
// providers do not each maintain their own GQL wiring.
package anilist

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"kari/internal/config"
	"kari/internal/httpclient"
	"kari/internal/provider"
)

// Title holds the multiple localised name variants AniList exposes for a
// single media entry. At least one field is non-empty for any real entry.
type Title struct {
	Romaji        string `json:"romaji"`
	English       string `json:"english"`
	UserPreferred string `json:"userPreferred"`
	Native        string `json:"native"`
}

// Media is the AniList representation of one anime entry, combining stable
// identifiers with scheduling and episode-count information.
type Media struct {
	ID         int      `json:"id"`
	IDMal      int      `json:"idMal"`
	Title      Title    `json:"title"`
	Format     string   `json:"format"`
	Status     string   `json:"status"`
	SeasonYear int      `json:"seasonYear"`
	Synonyms   []string `json:"synonyms"`
	// Episodes is the total episode count as declared by AniList; 0 when
	// the series is still airing or the count is unknown.
	Episodes int `json:"episodes"`
	// NextAiringEpisode is the next episode number when the series is
	// currently airing; 0 otherwise.
	NextAiringEpisode int `json:"-"`
}

// EpisodeCount returns the best available episode count. When the declared
// total is known (Episodes > 0) it is returned directly. For currently-airing
// series the last confirmed aired episode is NextAiringEpisode−1; a negative
// result is clamped to 0.
func (m Media) EpisodeCount() int {
	if m.Episodes > 0 {
		return m.Episodes
	}
	n := m.NextAiringEpisode - 1
	if n < 0 {
		return 0
	}
	return n
}

// PickTitle selects the most user-friendly title from t, preferring English,
// then UserPreferred, then Romaji, then Native. Each candidate is trimmed
// before comparison; an empty string is returned when all fields are blank.
func PickTitle(t Title) string {
	for _, s := range []string{t.English, t.UserPreferred, t.Romaji, t.Native} {
		if v := strings.TrimSpace(s); v != "" {
			return v
		}
	}
	return ""
}

// Search queries AniList for anime matching query and returns up to 20
// results ordered by search relevance. Format MOVIE entries are tagged as
// provider.MediaTypeMovie; all others become provider.MediaTypeAnime. Entries
// with no resolvable title are silently skipped.
func Search(ctx context.Context, hc *http.Client, query string) ([]provider.SearchResult, error) {
	return SearchWithEndpoint(ctx, hc, query, config.AniListAPIBase)
}

// SearchWithEndpoint is Search against an explicit GraphQL endpoint. The
// production path always uses config.AniListAPIBase; the parameter exists
// so offline tests can serve canned GraphQL payloads.
func SearchWithEndpoint(ctx context.Context, hc *http.Client, query, endpoint string) ([]provider.SearchResult, error) {
	const gql = `query($search:String){Page(page:1,perPage:20){media(search:$search,type:ANIME,sort:SEARCH_MATCH){id title{romaji english userPreferred native} seasonYear format}}}`

	body, err := gqlPost(ctx, hc, endpoint, gql, map[string]any{"search": query})
	if err != nil {
		return nil, err
	}

	var parsed struct {
		Data struct {
			Page struct {
				Media []struct {
					ID         int    `json:"id"`
					Title      Title  `json:"title"`
					SeasonYear int    `json:"seasonYear"`
					Format     string `json:"format"`
				} `json:"media"`
			} `json:"Page"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("anilist: decode search: %w", err)
	}

	results := make([]provider.SearchResult, 0, len(parsed.Data.Page.Media))
	for _, m := range parsed.Data.Page.Media {
		title := PickTitle(m.Title)
		if title == "" {
			continue
		}
		mediaType := provider.MediaTypeAnime
		if strings.EqualFold(m.Format, "MOVIE") {
			mediaType = provider.MediaTypeMovie
		}
		year := ""
		if m.SeasonYear > 0 {
			year = strconv.Itoa(m.SeasonYear)
		}
		results = append(results, provider.SearchResult{
			Title:     title,
			ID:        strconv.Itoa(m.ID),
			Type:      provider.ModeAnime,
			Year:      year,
			MediaType: mediaType,
		})
	}
	return results, nil
}

// FetchMedia retrieves full metadata for a single AniList media entry by its
// AniList ID. It returns provider.ErrNotFound when AniList returns a null
// Media field (e.g. the ID does not exist or is not of type ANIME).
func FetchMedia(ctx context.Context, hc *http.Client, anilistID int) (Media, error) {
	return FetchMediaWithEndpoint(ctx, hc, anilistID, config.AniListAPIBase)
}

// FetchMediaWithEndpoint is FetchMedia against an explicit GraphQL endpoint.
// The production path always uses config.AniListAPIBase; the parameter
// exists so offline tests can serve canned GraphQL payloads.
func FetchMediaWithEndpoint(ctx context.Context, hc *http.Client, anilistID int, endpoint string) (Media, error) {
	const gql = `query($id:Int){Media(id:$id,type:ANIME){id idMal title{romaji english userPreferred native} synonyms seasonYear format episodes status nextAiringEpisode{episode}}}`

	body, err := gqlPost(ctx, hc, endpoint, gql, map[string]any{"id": anilistID})
	if err != nil {
		return Media{}, err
	}

	var parsed struct {
		Data struct {
			Media *struct {
				ID                int      `json:"id"`
				IDMal             int      `json:"idMal"`
				Title             Title    `json:"title"`
				Format            string   `json:"format"`
				Status            string   `json:"status"`
				SeasonYear        int      `json:"seasonYear"`
				Episodes          int      `json:"episodes"`
				Synonyms          []string `json:"synonyms"`
				NextAiringEpisode *struct {
					Episode int `json:"episode"`
				} `json:"nextAiringEpisode"`
			} `json:"Media"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return Media{}, fmt.Errorf("anilist: decode media: %w", err)
	}
	if parsed.Data.Media == nil {
		return Media{}, provider.ErrNotFound
	}

	m := parsed.Data.Media
	out := Media{
		ID:         m.ID,
		IDMal:      m.IDMal,
		Title:      m.Title,
		Format:     m.Format,
		Status:     m.Status,
		SeasonYear: m.SeasonYear,
		Episodes:   m.Episodes,
		Synonyms:   m.Synonyms,
	}
	if m.NextAiringEpisode != nil {
		out.NextAiringEpisode = m.NextAiringEpisode.Episode
	}
	return out, nil
}

// gqlPost encodes a GraphQL request body, POSTs it to endpoint, and
// returns the raw response bytes. A non-200 status is mapped to
// *provider.HTTPError.
func gqlPost(ctx context.Context, hc *http.Client, endpoint, gql string, vars map[string]any) ([]byte, error) {

	payload, err := json.Marshal(map[string]any{
		"query":     gql,
		"variables": vars,
	})
	if err != nil {
		return nil, fmt.Errorf("anilist: marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(string(payload)))
	if err != nil {
		return nil, fmt.Errorf("anilist: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", config.DesktopUserAgent)
	req.Header.Set("Referer", config.AniListAuthBase+"/")
	req.Header.Set("Origin", config.AniListAuthBase)

	resp, err := hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("anilist: do request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, &provider.HTTPError{Code: resp.StatusCode, URL: endpoint}
	}

	body, err := httpclient.ReadCapped(resp)
	if err != nil {
		return nil, fmt.Errorf("anilist: read body: %w", err)
	}
	return body, nil
}
