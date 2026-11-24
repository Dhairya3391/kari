// Package way2movies implements provider.Provider for Way2Movies
// (https://way2movies.vip), a TMDB-keyed catalog of movies, television
// series, and animated content.
//
// Catalog search and episode listings reuse the shared TMDB-keyed streambase.
// Playback fans out over the site's own scraper API, which returns direct HLS
// playlists for each server that carries the requested title. The scraper
// authorizes only requests bearing the Way2Movies origin, supplied as both
// Origin and Referer. Every returned playlist is directly playable by mpv.
package way2movies

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"kari/internal/config"
	"kari/internal/httpclient"
	"kari/internal/provider"
	"kari/internal/provider/streambase"
	"kari/internal/tmdb"
)

var (
	_ provider.Provider      = (*Client)(nil)
	_ provider.Presenter     = (*Client)(nil)
	_ provider.EpisodeSource = (*Client)(nil)
)

// Client implements provider.Provider against the Way2Movies catalog and its
// scraper API.
type Client struct {
	base        *streambase.Base
	httpClient  *http.Client
	scraperBase string
	origin      string
}

// NewClient constructs the Way2Movies provider with the shared TMDB search
// base and the production scraper endpoint.
func NewClient(keyPool *tmdb.KeyPool) (*Client, error) {
	return NewClientWithURL(keyPool, config.Way2MoviesScraperBase, config.Way2MoviesOrigin)
}

// NewClientWithURL constructs the Way2Movies provider with custom upstream
// endpoints, allowing offline tests to mock the scraper without live traffic.
func NewClientWithURL(keyPool *tmdb.KeyPool, scraperBase, origin string) (*Client, error) {
	base, err := streambase.New(keyPool)
	if err != nil {
		return nil, fmt.Errorf("way2movies streambase: %w", err)
	}
	return &Client{
		base:        base,
		httpClient:  httpclient.NewWithUserAgent(config.DesktopUserAgent),
		scraperBase: strings.TrimRight(scraperBase, "/"),
		origin:      strings.TrimRight(origin, "/"),
	}, nil
}

// Alias implements provider.Presenter with the display name shown in the UI.
func (c *Client) Alias() string { return "Way2Movies" }

// Name implements provider.Provider with the canonical provider identifier.
func (c *Client) Name() string { return "way2movies" }

// Modes implements provider.Provider, declaring support for movies, TV series,
// and cartoon/animation modes.
func (c *Client) Modes() []provider.Mode {
	return []provider.Mode{
		{Name: provider.ModeMovies, Priority: 2},
		{Name: provider.ModeTV, Priority: 2},
		{Name: provider.ModeCartoon, Priority: 2},
	}
}

// Search delegates title resolution to the shared TMDB-backed streambase.
func (c *Client) Search(ctx context.Context, query string, mode provider.ContentType) ([]provider.SearchResult, error) {
	return c.base.Search(ctx, query, mode)
}

// FetchEpisodes delegates episode and season lookups to the shared TMDB-backed
// streambase.
func (c *Client) FetchEpisodes(ctx context.Context, series provider.SearchResult) ([]provider.Episode, error) {
	return c.base.FetchEpisodes(ctx, series)
}

// ResolveSource resolves playable media sources from every scraper server that
// carries the requested title.
func (c *Client) ResolveSource(ctx context.Context, mediaID string, episode provider.Episode) ([]provider.MediaSource, error) {
	tmdbID, mediaType, season, epNum, err := parseCoordinates(mediaID, episode)
	if err != nil {
		return nil, err
	}
	sources, err := c.fetchSources(ctx, tmdbID, mediaType, season, epNum)
	if err != nil {
		return nil, err
	}
	sources = provider.FilterDirectSources(sources)
	if len(sources) == 0 {
		return nil, provider.ErrNoSources
	}
	return sources, nil
}
