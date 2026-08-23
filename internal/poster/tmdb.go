package poster

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"

	"kari/internal/config"
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

		target := fmt.Sprintf("%s/%s/%d?api_key=%s", config.TMDBAPIBase, endpoint, tmdbID, url.QueryEscape(apiKey))
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
		rating = fmt.Sprintf("★ %.1f/10", details.VoteAverage)
	}
	return Details{Overview: details.Overview, Genres: genres, Rating: rating}, nil
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
