package poster

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"kari/internal/config"
	"kari/internal/provider/kit"
)

type tmdbDetails struct {
	PosterPath  string  `json:"poster_path"`
	Overview    string  `json:"overview"`
	VoteAverage float64 `json:"vote_average"`
	Genres      []struct {
		Name string `json:"name"`
	} `json:"genres"`
}

// fetchTMDBMediaDetails fetches a movie/tv's details from TMDB, rotating API
// keys on auth failure the same way internal/provider/streambase does. Both
// the poster URL and the on-screen overview/genres are derived from this one
// response, since TMDB returns all of it together. The response is cached
// and concurrent callers for the same (tmdbID, mediaType) — e.g. the poster
// and details fetches the TUI fires together for a preview screen — are
// collapsed into a single HTTP request via c.sf, so opening a title's
// preview never costs two identical TMDB round-trips.
func (c *Client) fetchTMDBMediaDetails(ctx context.Context, tmdbID int, mediaType string) (tmdbDetails, error) {
	endpoint := "movie"
	if mediaType == "tv" || mediaType == "anime" {
		endpoint = "tv"
	}

	key := fmt.Sprintf("%d-%s", tmdbID, endpoint)
	if details, ok := c.rawCache.Get(key); ok {
		return details, nil
	}

	v, err, _ := c.sf.Do(key, func() (any, error) {
		details, ferr := c.fetchTMDBMediaDetailsUncached(ctx, tmdbID, endpoint)
		if ferr != nil {
			return tmdbDetails{}, ferr
		}
		c.rawCache.Set(key, details)
		return details, nil
	})
	if err != nil {
		return tmdbDetails{}, err
	}
	return v.(tmdbDetails), nil
}

// tmdbAPIBase returns the TMDB API root, preferring a test override.
func (c *Client) tmdbAPIBase() string {
	if c.tmdbBase != "" {
		return c.tmdbBase
	}
	return config.TMDBAPIBase
}

func (c *Client) fetchTMDBMediaDetailsUncached(ctx context.Context, tmdbID int, endpoint string) (tmdbDetails, error) {
	var lastAuthErr error
	for {
		apiKey, err := c.keyPool.NextKey()
		if err != nil {
			if lastAuthErr != nil {
				return tmdbDetails{}, fmt.Errorf("poster: tmdb auth failed after key rotation: %w", lastAuthErr)
			}
			return tmdbDetails{}, err
		}

		target := fmt.Sprintf("%s/%s/%d?api_key=%s", c.tmdbAPIBase(), endpoint, tmdbID, url.QueryEscape(apiKey))
		details, status, err := fetchTMDBDetails(ctx, c.http, target)
		if err == nil {
			return details, nil
		}
		if status != http.StatusUnauthorized && status != http.StatusTooManyRequests {
			return tmdbDetails{}, err
		}
		posterLog.Warn("tmdb request unauthorized; rotating key", "tmdbID", tmdbID, "err", err)
		c.keyPool.MarkFailed(apiKey)
		lastAuthErr = err
	}
}

func (c *Client) tmdbPosterURL(ctx context.Context, tmdbID int, mediaType string) (string, error) {
	details, err := c.fetchTMDBMediaDetails(ctx, tmdbID, mediaType)
	if err != nil {
		return "", err
	}
	if details.PosterPath == "" {
		return "", nil
	}
	return config.TMDBImageBase + tmdbPosterSize + details.PosterPath, nil
}

func (c *Client) tmdbDetailsInfo(ctx context.Context, tmdbID int, mediaType string) (Details, error) {
	details, err := c.fetchTMDBMediaDetails(ctx, tmdbID, mediaType)
	if err != nil {
		return Details{}, err
	}
	genres := make([]string, 0, len(details.Genres))
	for _, g := range details.Genres {
		genres = append(genres, g.Name)
	}
	rating := ""
	if details.VoteAverage > 0 {
		rating = fmt.Sprintf("%.1f/10", details.VoteAverage)
	}
	return Details{Overview: details.Overview, Genres: genres, Rating: rating}, nil
}

// tmdbSearchTVResponse is the subset of /search/tv used to pick a TV id.
type tmdbSearchTVResponse struct {
	Results []struct {
		ID int `json:"id"`
	} `json:"results"`
}

// tmdbSeasonsResponse is the subset of /tv/{id} listing season sizes; the
// counts let us turn per-season episode numbers into absolute ones.
type tmdbSeasonsResponse struct {
	Seasons []struct {
		SeasonNumber int `json:"season_number"`
		EpisodeCount int `json:"episode_count"`
	} `json:"seasons"`
}

// tmdbEpisode is one episode row from a TMDB season listing.
type tmdbEpisode struct {
	EpisodeNumber int    `json:"episode_number"`
	Name          string `json:"name"`
}

type tmdbSeasonResponse struct {
	Episodes []tmdbEpisode `json:"episodes"`
}

// FetchEpisodeTitlesTMDB returns real episode titles for a series, keyed by
// absolute episode number. It resolves the title to a TMDB show (optionally
// narrowed by year) and walks every regular season in order. TMDB numbers
// episodes inconsistently across shows — some (Naruto, One Piece) already use
// absolute numbers that continue across seasons, while others (Bleach)
// restart at 1 each season — so it detects which scheme a show uses and keys
// accordingly. This backfills the episodes AniList's streamingEpisodes leave
// unnamed. It is best-effort: callers treat every error as "no fallback".
func (c *Client) FetchEpisodeTitlesTMDB(ctx context.Context, title string, year int) (map[int]string, error) {
	title = strings.TrimSpace(title)
	if title == "" {
		return nil, fmt.Errorf("poster: tmdb episode titles require a title")
	}
	tvID, err := c.searchTMDBTV(ctx, title, year)
	if err != nil {
		return nil, err
	}

	var show tmdbSeasonsResponse
	if err := c.tmdbGet(ctx, fmt.Sprintf("/tv/%d", tvID), nil, &show); err != nil {
		return nil, err
	}

	// If the title requests a specific season (e.g. "Season 2"), and TMDB's show
	// has that season, return only that season's episodes starting at 1.
	if reqSeason := kit.ParseSeason(title); reqSeason > 1 {
		for _, season := range show.Seasons {
			if season.SeasonNumber == reqSeason {
				var sr tmdbSeasonResponse
				if err := c.tmdbGet(ctx, fmt.Sprintf("/tv/%d/season/%d", tvID, reqSeason), nil, &sr); err != nil {
					return nil, err
				}
				out := make(map[int]string)
				for _, ep := range sr.Episodes {
					if ep.EpisodeNumber > 0 && strings.TrimSpace(ep.Name) != "" {
						out[ep.EpisodeNumber] = ep.Name
					}
				}
				if len(out) > 0 {
					return out, nil
				}
			}
		}
	}
	// Fetch episodes first: the numbering scheme isn't known until we can
	// compare a season's first episode number against 1.
	type seasonData struct {
		count    int
		episodes []tmdbEpisode
	}
	var seasons []seasonData
	for _, season := range show.Seasons {
		if season.SeasonNumber <= 0 {
			continue // specials aren't part of the absolute episode order
		}
		var sr tmdbSeasonResponse
		if err := c.tmdbGet(ctx, fmt.Sprintf("/tv/%d/season/%d", tvID, season.SeasonNumber), nil, &sr); err != nil {
			return nil, err
		}
		seasons = append(seasons, seasonData{count: season.EpisodeCount, episodes: sr.Episodes})
	}

	// A later season starting above episode 1 means the show already numbers
	// absolutely; otherwise each season restarts and needs the running offset.
	absolute := false
	if len(seasons) > 1 {
		for _, ep := range seasons[1].episodes {
			if ep.EpisodeNumber > 0 {
				absolute = ep.EpisodeNumber != 1
				break
			}
		}
	}

	out := make(map[int]string)
	offset := 0
	for _, season := range seasons {
		for _, ep := range season.episodes {
			if ep.EpisodeNumber <= 0 || strings.TrimSpace(ep.Name) == "" {
				continue
			}
			num := ep.EpisodeNumber
			if !absolute {
				num += offset
			}
			out[num] = ep.Name
		}
		offset += season.count
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("poster: no tmdb episode titles for %q", title)
	}
	return out, nil
}

// searchTMDBTV resolves a series title to a TMDB TV id, retrying without the
// year when the first pass finds nothing (provider titles are often localized,
// or the year is a later season's air date rather than the show's).
func (c *Client) searchTMDBTV(ctx context.Context, title string, year int) (int, error) {
	params := url.Values{}
	params.Set("query", title)
	if year > 0 {
		params.Set("first_air_date_year", strconv.Itoa(year))
	}
	var res tmdbSearchTVResponse
	if err := c.tmdbGet(ctx, "/search/tv", params, &res); err != nil {
		return 0, err
	}
	if len(res.Results) == 0 && year > 0 {
		params.Del("first_air_date_year")
		res = tmdbSearchTVResponse{}
		if err := c.tmdbGet(ctx, "/search/tv", params, &res); err != nil {
			return 0, err
		}
	}
	if len(res.Results) == 0 {
		return 0, fmt.Errorf("poster: no tmdb match for %q", title)
	}
	return res.Results[0].ID, nil
}

// tmdbGet decodes one TMDB GET into out, rotating API keys on auth/rate
// failures the same way fetchTMDBMediaDetailsUncached does.
func (c *Client) tmdbGet(ctx context.Context, path string, params url.Values, out any) error {
	if params == nil {
		params = url.Values{}
	}
	var lastAuthErr error
	for {
		apiKey, err := c.keyPool.NextKey()
		if err != nil {
			if lastAuthErr != nil {
				return fmt.Errorf("poster: tmdb auth failed after key rotation: %w", lastAuthErr)
			}
			return err
		}
		params.Set("api_key", apiKey)
		target := c.tmdbAPIBase() + path + "?" + params.Encode()
		status, err := getTMDBJSON(ctx, c.http, target, out)
		if err == nil {
			return nil
		}
		if status != http.StatusUnauthorized && status != http.StatusTooManyRequests {
			return err
		}
		posterLog.Warn("tmdb request unauthorized; rotating key", "path", path, "err", err)
		c.keyPool.MarkFailed(apiKey)
		lastAuthErr = err
	}
}

// getTMDBJSON performs one TMDB request and decodes a 2xx body into out.
func getTMDBJSON(ctx context.Context, client *http.Client, target string, out any) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return resp.StatusCode, fmt.Errorf("poster: tmdb status %d", resp.StatusCode)
	}
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return resp.StatusCode, err
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return resp.StatusCode, err
	}
	return resp.StatusCode, nil
}

func fetchTMDBDetails(ctx context.Context, client *http.Client, target string) (tmdbDetails, int, error) {
	var zero tmdbDetails
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return zero, 0, err
	}
	req.Header.Set("Accept", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return zero, 0, err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return zero, resp.StatusCode, fmt.Errorf("poster: tmdb status %d", resp.StatusCode)
	}

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return zero, resp.StatusCode, err
	}
	var details tmdbDetails
	if err := json.Unmarshal(raw, &details); err != nil {
		return zero, resp.StatusCode, err
	}
	return details, resp.StatusCode, nil
}
