package anivault

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"unicode"

	"kari/internal/provider"
	"kari/internal/provider/anilist"
	"kari/internal/provider/kit"
)

type catalogIdentityResponse struct {
	OK     bool `json:"ok"`
	Mapped bool `json:"mapped"`
	Data   struct {
		ShortID string `json:"shortId"`
	} `json:"data"`
}

type catalogTitle struct {
	ShortID      string   `json:"shortId"`
	Title        string   `json:"title"`
	TitleEnglish string   `json:"titleEnglish"`
	TitleRomaji  string   `json:"titleRomaji"`
	Synonyms     []string `json:"synonyms"`
}

type catalogSearchResponse struct {
	OK   bool           `json:"ok"`
	Data []catalogTitle `json:"data"`
}

type catalogDetailResponse struct {
	OK   bool `json:"ok"`
	Data *struct {
		Episodes *int `json:"episodes"`
	} `json:"data"`
}

type catalogEpisode struct {
	Number float64 `json:"number"`
	Title  string  `json:"title"`
	Filler bool    `json:"filler"`
}

type catalogEpisodesResponse struct {
	OK   bool             `json:"ok"`
	Data []catalogEpisode `json:"data"`
}

// Search preserves AniList's canonical identifiers; Hoshika IDs are resolved
// only when a title is selected, never advertised as AniList IDs.
func (c *Client) Search(ctx context.Context, query string, _ provider.ContentType) ([]provider.SearchResult, error) {
	results, err := anilist.SearchWithEndpoint(
		ctx,
		c.http,
		query,
		c.anilistEndpoint,
	)
	if err != nil {
		return nil, fmt.Errorf("search catalog: %w", err)
	}
	if len(results) == 0 {
		return nil, fmt.Errorf("search catalog: %w", provider.ErrNoResults)
	}
	return results, nil
}

// FetchEpisodes uses the detail count because Hoshika's episode metadata
// endpoint returns only a partial roster. Audio availability is resolved later.
func (c *Client) FetchEpisodes(ctx context.Context, series provider.SearchResult) ([]provider.Episode, error) {
	shortID, err := c.resolveTitle(ctx, series.ID)
	if err != nil {
		return nil, fmt.Errorf("fetch episodes: %w", err)
	}
	path := "/api/hoshika/anime/" + shortID
	body, err := c.get(ctx, path)
	if err != nil {
		return nil, fmt.Errorf("fetch anime detail: %w", err)
	}
	var detail catalogDetailResponse
	if err := json.Unmarshal(body, &detail); err != nil {
		return nil, fmt.Errorf("decode anime detail: %w: %w", provider.ErrUpstreamChanged, err)
	}
	if !detail.OK || detail.Data == nil {
		return nil, fmt.Errorf("fetch anime detail: %w", provider.ErrUpstreamChanged)
	}
	count := 0
	if detail.Data.Episodes != nil {
		count = *detail.Data.Episodes
	}

	metadata, err := c.catalogEpisodeMetadata(ctx, path+"/episodes")
	if err != nil {
		return nil, err
	}
	byNumber := make(map[int]catalogEpisode, len(metadata))
	for _, entry := range metadata {
		number := int(entry.Number)
		// Integer watch routes cannot address fractional specials without
		// silently playing a neighbouring episode.
		if number <= 0 || entry.Number != float64(number) {
			continue
		}
		byNumber[number] = entry
		if number > count {
			count = number
		}
	}
	if count <= 0 {
		return nil, fmt.Errorf("fetch episodes: %w", provider.ErrNoEpisodes)
	}
	season := kit.ParseSeason(series.Title)
	if season <= 0 {
		season = 1
	}
	episodes := make([]provider.Episode, 0, count*2)
	for number := 1; number <= count; number++ {
		entry := byNumber[number]
		title := kit.Clean(entry.Title)
		if title == "" {
			title = fmt.Sprintf("Episode %d", number)
		}
		for _, audio := range []string{provider.AudioSub, provider.AudioDub} {
			episodes = append(episodes, provider.Episode{
				ID:      fmt.Sprintf("watch/anivault/hoshika/%s/%s/%d", shortID, audio, number),
				Title:   title,
				Season:  season,
				Episode: number,
				Audio:   audio,
				Filler:  entry.Filler,
			})
		}
	}
	return episodes, nil
}

func (c *Client) catalogEpisodeMetadata(ctx context.Context, path string) ([]catalogEpisode, error) {
	body, err := c.get(ctx, path)
	if errors.Is(err, provider.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("fetch episode metadata: %w", err)
	}
	var payload catalogEpisodesResponse
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("decode episode metadata: %w: %w", provider.ErrUpstreamChanged, err)
	}
	if !payload.OK || payload.Data == nil {
		return nil, fmt.Errorf("fetch episode metadata: %w", provider.ErrUpstreamChanged)
	}
	return payload.Data, nil
}

// resolveTitle keeps plain numeric IDs in the AniList namespace. Only an
// explicit hoshika/ prefix bypasses the canonical identity lookup.
func (c *Client) resolveTitle(ctx context.Context, mediaID string) (string, error) {
	mediaID = strings.TrimSpace(mediaID)
	if shortID, ok := strings.CutPrefix(mediaID, "hoshika/"); ok {
		if !validCatalogID(shortID) {
			return "", fmt.Errorf("resolve hoshika id %q: %w", mediaID, provider.ErrNotFound)
		}
		return shortID, nil
	}
	if !validCatalogID(mediaID) {
		return "", fmt.Errorf("resolve anilist id %q: %w", mediaID, provider.ErrNotFound)
	}
	anilistID, _ := strconv.Atoi(mediaID)
	media, err := anilist.FetchMediaWithEndpoint(
		ctx,
		c.http,
		anilistID,
		c.anilistEndpoint,
	)
	if err != nil {
		return "", fmt.Errorf("resolve anilist media: %w", err)
	}
	if media.IDMal > 0 {
		shortID, err := c.catalogLegacyIdentity(ctx, media.IDMal)
		if err != nil {
			return "", err
		}
		if shortID != "" {
			return shortID, nil
		}
	}
	return c.catalogMatchTitle(ctx, media)
}

func (c *Client) catalogLegacyIdentity(ctx context.Context, malID int) (string, error) {
	body, err := c.get(ctx, "/api/hoshika/identity-by-legacy/"+strconv.Itoa(malID))
	if errors.Is(err, provider.ErrNotFound) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("resolve legacy identity: %w", err)
	}
	var payload catalogIdentityResponse
	if err := json.Unmarshal(body, &payload); err != nil {
		return "", fmt.Errorf("decode legacy identity: %w: %w", provider.ErrUpstreamChanged, err)
	}
	if !payload.Mapped {
		return "", nil
	}
	if !payload.OK || !validCatalogID(payload.Data.ShortID) {
		return "", fmt.Errorf("resolve legacy identity: %w", provider.ErrUpstreamChanged)
	}
	return payload.Data.ShortID, nil
}

func (c *Client) catalogMatchTitle(ctx context.Context, media anilist.Media) (string, error) {
	titles := append([]string{media.Title.English, media.Title.Romaji}, media.Synonyms...)
	names := make(map[string]bool, len(titles))
	queries := make([]string, 0, len(titles))
	for _, title := range titles {
		normalized := normalizeCatalogTitle(title)
		if normalized == "" || names[normalized] {
			continue
		}
		names[normalized] = true
		queries = append(queries, strings.TrimSpace(title))
	}
	for _, query := range queries {
		body, err := c.get(ctx, "/api/hoshika/search?q="+url.QueryEscape(query)+"&limit=20")
		if err != nil {
			return "", fmt.Errorf("search hoshika title: %w", err)
		}
		var payload catalogSearchResponse
		if err := json.Unmarshal(body, &payload); err != nil {
			return "", fmt.Errorf("decode hoshika search: %w: %w", provider.ErrUpstreamChanged, err)
		}
		if !payload.OK || payload.Data == nil {
			return "", fmt.Errorf("search hoshika title: %w", provider.ErrUpstreamChanged)
		}
		match := ""
		for _, candidate := range payload.Data {
			if !catalogTitleMatches(candidate, names) {
				continue
			}
			if !validCatalogID(candidate.ShortID) {
				return "", fmt.Errorf("match hoshika title: %w", provider.ErrUpstreamChanged)
			}
			if match != "" && match != candidate.ShortID {
				return "", fmt.Errorf("ambiguous hoshika title: %w", provider.ErrNoMatch)
			}
			match = candidate.ShortID
		}
		if match != "" {
			return match, nil
		}
	}
	return "", fmt.Errorf("match hoshika title: %w", provider.ErrNoMatch)
}

func catalogTitleMatches(candidate catalogTitle, names map[string]bool) bool {
	titles := append([]string{candidate.Title, candidate.TitleEnglish, candidate.TitleRomaji}, candidate.Synonyms...)
	for _, title := range titles {
		if names[normalizeCatalogTitle(title)] {
			return true
		}
	}
	return false
}

func normalizeCatalogTitle(title string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return unicode.ToLower(r)
		}
		return -1
	}, title)
}

func validCatalogID(id string) bool {
	if id == "" {
		return false
	}
	for _, r := range id {
		if r < '0' || r > '9' {
			return false
		}
	}
	number, err := strconv.Atoi(id)
	return err == nil && number > 0
}
