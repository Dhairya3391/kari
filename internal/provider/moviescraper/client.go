package moviescraper

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"

	"kari/internal/config"
	"kari/internal/httpclient"
	"kari/internal/provider"
	"kari/internal/provider/streambase"
	"kari/internal/tmdb"
)

const (
	movieScraperAPI = config.MovieScraperAPI
	movieScraperUA  = config.DesktopUserAgent
)

type Client struct {
	base       *streambase.Base
	httpClient *http.Client
}

type MovieScraperSource struct {
	Quality string `json:"q"`
	URL     string `json:"u"`
}

func NewClient(keyPool *tmdb.KeyPool) (*Client, error) {
	base, err := streambase.New(keyPool)
	if err != nil {
		return nil, err
	}
	return &Client{
		base:       base,
		httpClient: httpclient.NewWithUserAgent(movieScraperUA),
	}, nil
}

func (c *Client) Name() string { return "moviescraper" }

func (c *Client) Modes() []provider.Mode {
	return []provider.Mode{{Name: provider.ModeMovies, Priority: 3}, {Name: provider.ModeTV, Priority: 3}}
}

func (c *Client) Search(ctx context.Context, query string, mode provider.ContentType) ([]provider.SearchResult, error) {
	return c.base.Search(ctx, query, mode)
}

func (c *Client) FetchEpisodes(ctx context.Context, series provider.SearchResult) ([]provider.Episode, error) {
	return c.base.FetchEpisodes(ctx, series)
}

func (c *Client) ResolveSource(ctx context.Context, mediaID string, episode provider.Episode) ([]provider.MediaSource, error) {
	tmdbID := episode.TMDBID
	if tmdbID <= 0 {
		var err error
		tmdbID, err = strconv.Atoi(mediaID)
		if err != nil {
			return nil, fmt.Errorf("invalid media ID: %w", err)
		}
	}
	sources, err := c.FetchMovieScraperSources(ctx, tmdbID, episode.Season, episode.Episode)
	if err != nil {
		return nil, err
	}
	if len(sources) == 0 {
		return nil, provider.ErrNoSources
	}
	out := make([]provider.MediaSource, 0, len(sources))
	for i, src := range sources {
		out = append(out, provider.MediaSource{
			URL:       src.URL,
			Quality:   fmt.Sprintf("[MOVIESCRAPER] Source %d", i+1),
			UserAgent: movieScraperUA,
		})
	}
	return out, nil
}

func (c *Client) FetchMovieScraperSources(ctx context.Context, tmdbID int, season, episode int) ([]MovieScraperSource, error) {
	if tmdbID <= 0 {
		return nil, fmt.Errorf("missing tmdb id")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, movieScraperAPI, nil)
	if err != nil {
		return nil, err
	}

	q := req.URL.Query()
	q.Set("id", strconv.Itoa(tmdbID))
	if season > 0 {
		q.Set("s", strconv.Itoa(season))
	}
	if episode > 0 {
		q.Set("e", strconv.Itoa(episode))
	}
	req.URL.RawQuery = q.Encode()
	req.Header.Set("User-Agent", movieScraperUA)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, &provider.HTTPError{Code: resp.StatusCode, URL: req.URL.String()}
	}

	var firstResult struct {
		URL   string `json:"url"`
		Error string `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&firstResult); err != nil {
		return nil, fmt.Errorf("parse first response: %w", err)
	}
	if firstResult.Error != "" {
		return nil, fmt.Errorf("api error: %s", firstResult.Error)
	}
	if firstResult.URL == "" {
		return nil, provider.ErrNoSources
	}

	// Step 2: Get the JSON from vidlink via proxy
	proxyReq, err := http.NewRequestWithContext(ctx, http.MethodGet, movieScraperAPI+"?url="+url.QueryEscape(firstResult.URL), nil)
	if err != nil {
		return nil, err
	}
	proxyReq.Header.Set("User-Agent", movieScraperUA)

	proxyResp, err := c.httpClient.Do(proxyReq)
	if err != nil {
		return nil, err
	}
	defer proxyResp.Body.Close()
	if proxyResp.StatusCode >= 400 {
		return nil, &provider.HTTPError{Code: proxyResp.StatusCode, URL: proxyReq.URL.String()}
	}

	var secondResult struct {
		Stream struct {
			Playlist string `json:"playlist"`
		} `json:"stream"`
	}
	if err := json.NewDecoder(proxyResp.Body).Decode(&secondResult); err != nil {
		return nil, fmt.Errorf("parse second response: %w", err)
	}

	if secondResult.Stream.Playlist == "" {
		return nil, provider.ErrNoSources
	}

	finalURL := movieScraperAPI + "?url=" + url.QueryEscape(secondResult.Stream.Playlist)
	return []MovieScraperSource{{Quality: "auto", URL: finalURL}}, nil
}

var _ provider.Provider = (*Client)(nil)
