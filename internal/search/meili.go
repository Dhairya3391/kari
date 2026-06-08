package search

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"kari/internal/config"
	"kari/internal/httpclient"
	"kari/internal/model"
	"kari/internal/provider"
)

type Result struct {
	TMDBID        int    `json:"tmdb_id"`
	Title         string `json:"title"`
	OriginalTitle string `json:"original_title"`
	MediaType     string `json:"media_type"`
	Year          any    `json:"year"`
	EpisodeCount  *int   `json:"episode_count"`
	Language      string `json:"original_language"`
}

type Response struct {
	Query          string   `json:"query"`
	CorrectedQuery string   `json:"corrected_query,omitempty"`
	Total          int      `json:"total"`
	Results        []Result `json:"results"`
}

type Client struct {
	httpClient *http.Client
}

func NewClient() *Client {
	return &Client{
		httpClient: httpclient.NewWithUserAgent(config.DesktopUserAgent),
	}
}

func (c *Client) Search(ctx context.Context, query string) ([]model.SearchResult, error) {
	return c.SearchWithEndpoint(ctx, query, "/search")
}

func (c *Client) SearchMovies(ctx context.Context, query string) ([]model.SearchResult, error) {
	return c.SearchWithEndpoint(ctx, query, "/movies")
}

func (c *Client) SearchTV(ctx context.Context, query string) ([]model.SearchResult, error) {
	return c.SearchWithEndpoint(ctx, query, "/tv")
}

func (c *Client) SearchWithEndpoint(ctx context.Context, query string, endpoint string) ([]model.SearchResult, error) {
	if query == "" {
		return nil, fmt.Errorf("empty query")
	}

	req, err := http.NewRequestWithContext(ctx, "GET", config.SearchAPIBase+endpoint, nil)
	if err != nil {
		return nil, err
	}

	q := req.URL.Query()
	q.Set("q", query)
	q.Set("limit", "30")
	req.URL.RawQuery = q.Encode()

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("API returned status %d", resp.StatusCode)
	}

	var result Response
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}
	if len(result.Results) == 0 {
		return nil, provider.ErrNoResults
	}

	out := make([]model.SearchResult, 0, len(result.Results))
	for _, r := range result.Results {
		yearStr := ""
		if r.Year != nil {
			yearStr = fmt.Sprintf("%v", r.Year)
		}
		out = append(out, model.SearchResult{
			Title:     r.Title,
			URL:       fmt.Sprintf("%s/%s/%d", config.TMDBBaseURL, r.MediaType, r.TMDBID),
			Provider:  "tmdb",
			MediaType: r.MediaType,
			Year:      yearStr,
			TMDBID:    r.TMDBID,
		})
	}

	return out, nil
}
