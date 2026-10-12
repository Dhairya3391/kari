// Package cinecat implements provider.Provider for Cinecat (https://beta.cinecat.eu),
// a streaming web application for movies, television series, and animated content.
//
// Catalog search and episode listings reuse the shared TMDB-keyed streambase.
// Playback streams resolve directly from Cinecat's primary HLS content delivery network
// (zcdn.bluevelvet.space) across adaptive tiers (4K, 1080p, 720p, 480p), enriched with
// multi-language subtitles from the Granite subtitle service.
//
// Every returned HLS playlist is directly playable by mpv without local helper proxies.
package cinecat

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
	_ provider.Provider          = (*Client)(nil)
	_ provider.Presenter         = (*Client)(nil)
	_ provider.StreamingProvider = (*Client)(nil)
)

// Client implements provider.Provider against the Cinecat catalog and CDN.
type Client struct {
	base       *streambase.Base
	httpClient *http.Client
	cdnBase    string
	subsBase   string
	siteBase   string
}

// NewClient constructs the Cinecat provider using the shared TMDB search base
// and production CDN endpoints.
func NewClient(keyPool *tmdb.KeyPool) (*Client, error) {
	return NewClientWithURLs(
		keyPool,
		config.CinecatCDNBase,
		config.CinecatSubsBase,
		config.CinecatBase,
	)
}

// NewClientWithURLs constructs the Cinecat provider with custom upstream endpoints,
// allowing offline tests to mock CDN and subtitle servers without network access.
func NewClientWithURLs(keyPool *tmdb.KeyPool, cdnBase, subsBase, siteBase string) (*Client, error) {
	base, err := streambase.New(keyPool)
	if err != nil {
		return nil, fmt.Errorf("cinecat streambase: %w", err)
	}
	return &Client{
		base:       base,
		httpClient: httpclient.NewWithUserAgent(config.DesktopUserAgent),
		cdnBase:    strings.TrimRight(cdnBase, "/"),
		subsBase:   strings.TrimRight(subsBase, "/"),
		siteBase:   strings.TrimRight(siteBase, "/"),
	}, nil
}

// Alias implements provider.Presenter with the display name shown in the UI.
func (c *Client) Alias() string { return "Cinecat" }

// Name implements provider.Provider with the canonical provider identifier.
func (c *Client) Name() string { return "cinecat" }

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

// FetchEpisodes delegates episode and season lookups to the shared TMDB-backed streambase.
func (c *Client) FetchEpisodes(ctx context.Context, series provider.SearchResult) ([]provider.Episode, error) {
	return c.base.FetchEpisodes(ctx, series)
}

// ResolveSource resolves playable media sources synchronously by collecting
// incremental arrivals from ResolveStream.
func (c *Client) ResolveSource(ctx context.Context, mediaID string, episode provider.Episode) ([]provider.MediaSource, error) {
	tmdbID, mediaType, season, epNum, err := parseCoordinates(mediaID, episode)
	if err != nil {
		return nil, err
	}

	ch := make(chan []provider.MediaSource, 8)
	var all []provider.MediaSource
	done := make(chan struct{})

	go func() {
		defer close(done)
		for batch := range ch {
			all = append(all, batch...)
		}
	}()

	err = c.fetchSources(ctx, tmdbID, mediaType, season, epNum, ch)
	close(ch)
	<-done

	if err != nil && len(all) == 0 {
		return nil, err
	}
	if len(all) == 0 {
		return nil, provider.ErrNoSources
	}
	return provider.FilterDirectSources(all), nil
}

// ResolveStream implements provider.StreamingProvider, pushing playable sources
// to updates as soon as streams and subtitles are prepared.
func (c *Client) ResolveStream(
	ctx context.Context,
	mediaID string,
	episode provider.Episode,
	updates chan<- []provider.MediaSource,
) error {
	tmdbID, mediaType, season, epNum, err := parseCoordinates(mediaID, episode)
	if err != nil {
		return err
	}
	return c.fetchSources(ctx, tmdbID, mediaType, season, epNum, updates)
}
