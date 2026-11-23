package vumeto

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
	"kari/internal/provider/kit"
)

// searchResponse is Vumeto's /api/search envelope.
type searchResponse struct {
	Items []catalogItem `json:"items"`
}

// titleResponse is Vumeto's /api/title envelope.
type titleResponse struct {
	Item catalogItem `json:"item"`
}

// catalogItem is one TMDB-backed title as Vumeto publishes it. Kind names the
// site section (movies, series, cartoons) and type is the TMDB shape.
type catalogItem struct {
	ID       int             `json:"id"`
	Kind     string          `json:"kind"`
	Type     string          `json:"type"`
	Title    string          `json:"title"`
	Year     string          `json:"year"`
	Overview string          `json:"overview"`
	Genres   []string        `json:"genres"`
	Poster   string          `json:"poster"`
	Seasons  []catalogSeason `json:"seasons"`
}

// catalogSeason is one season summary carried by a title response.
type catalogSeason struct {
	Number int `json:"number"`
}

// seasonResponse is Vumeto's /api/season envelope.
type seasonResponse struct {
	Episodes []seasonEpisode `json:"episodes"`
}

// seasonEpisode is one episode in a season listing.
type seasonEpisode struct {
	Number int    `json:"number"`
	Title  string `json:"title"`
}

// modeKind maps a Kari content mode onto Vumeto's catalog section name.
func modeKind(mode provider.ContentType) string {
	switch mode {
	case provider.ModeMovies:
		return "movies"
	case provider.ModeCartoon:
		return "cartoons"
	default:
		return "series"
	}
}

// mediaType maps a Vumeto type onto Kari's media vocabulary.
func mediaType(raw string) string {
	if strings.EqualFold(raw, provider.MediaTypeMovie) {
		return provider.MediaTypeMovie
	}
	return provider.MediaTypeTV
}

// getJSON fetches one JSON endpoint and decodes it into payload. Non-2xx
// responses become typed HTTP errors so callers can classify failures.
func (c *Client) getJSON(ctx context.Context, target string, headers map[string]string, payload any) error {
	body, err := c.get(ctx, target, headers)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(body, payload); err != nil {
		return fmt.Errorf("vumeto decode %s: %w", redactURL(target), err)
	}
	return nil
}

// get fetches one endpoint with the site's browser headers and returns the
// capped response body. A 404 is reported as ErrNotFound; other non-2xx
// statuses become HTTPError.
func (c *Client) get(ctx context.Context, target string, headers map[string]string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, fmt.Errorf("vumeto request: %w", err)
	}
	req.Header.Set("User-Agent", config.DesktopUserAgent)
	req.Header.Set("Accept", "application/json,text/html;q=0.9,*/*;q=0.8")
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("vumeto request %s: %w", redactURL(target), err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, provider.ErrNotFound
	}
	if resp.StatusCode != http.StatusOK {
		return nil, &provider.HTTPError{Code: resp.StatusCode, URL: redactURL(target)}
	}
	body, err := httpclient.ReadCapped(resp)
	if err != nil {
		return nil, fmt.Errorf("vumeto read %s: %w", redactURL(target), err)
	}
	return body, nil
}

// redactURL strips query strings so search terms never leak into errors or
// logs.
func redactURL(raw string) string {
	if i := strings.Index(raw, "?"); i >= 0 {
		return raw[:i]
	}
	return raw
}

// fetchTitle loads the season summaries for one TMDB id.
func (c *Client) fetchTitle(ctx context.Context, tmdbID int) (catalogItem, error) {
	target := fmt.Sprintf("%s/api/title?id=%d&type=%s", c.endpoints.Site, tmdbID, provider.MediaTypeTV)
	var payload titleResponse
	if err := c.getJSON(ctx, target, nil, &payload); err != nil {
		return catalogItem{}, fmt.Errorf("vumeto title: %w", err)
	}
	return payload.Item, nil
}

// seasonEpisodes lists every episode of every season from Vumeto's catalog.
func (c *Client) seasonEpisodes(ctx context.Context, tmdbID int) ([]provider.Episode, error) {
	item, err := c.fetchTitle(ctx, tmdbID)
	if err != nil {
		return nil, err
	}
	seasons := item.Seasons
	if len(seasons) == 0 {
		seasons = []catalogSeason{{Number: 1}}
	}
	var episodes []provider.Episode
	for _, season := range seasons {
		if season.Number <= 0 {
			continue
		}
		target := fmt.Sprintf("%s/api/season?id=%d&season=%d", c.endpoints.Site, tmdbID, season.Number)
		var payload seasonResponse
		if err := c.getJSON(ctx, target, nil, &payload); err != nil {
			return nil, fmt.Errorf("vumeto season %d: %w", season.Number, err)
		}
		for _, entry := range payload.Episodes {
			if entry.Number <= 0 {
				continue
			}
			title := kit.Clean(entry.Title)
			if title == "" {
				title = fmt.Sprintf("Episode %d", entry.Number)
			}
			episodes = append(episodes, provider.Episode{
				Title:   title,
				ID:      strconv.Itoa(tmdbID),
				Season:  season.Number,
				Episode: entry.Number,
				TMDBID:  tmdbID,
			})
		}
	}
	if len(episodes) == 0 {
		return nil, provider.ErrNoEpisodes
	}
	return episodes, nil
}
