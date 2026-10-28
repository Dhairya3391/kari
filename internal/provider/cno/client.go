// Package cno implements provider.Provider for Cno (https://cno.to),
// a high-definition streaming service for movies, TV series, and animation.
//
// Catalog search and episode listings reuse the shared TMDB-keyed streambase.
// Playback streams resolve directly from Cno's HLS infrastructure across
// multiple server tiers (Server 20 and Server 21) with adaptive multi-bitrate
// ladders up to 4K, enriched with OpenSubtitles WebVTT tracks.
//
// Every returned HLS playlist is directly playable by mpv without local helper proxies.
package cno

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

// Client implements provider.Provider against the Cno catalog and streaming servers.
type Client struct {
	base       *streambase.Base
	httpClient *http.Client
	siteBase   string
}

// NewClient constructs the Cno provider using the shared TMDB search base
// and production endpoints.
func NewClient(keyPool *tmdb.KeyPool) (*Client, error) {
	return NewClientWithURL(keyPool, config.CnoBase)
}

// NewClientWithURL constructs the Cno provider with a custom upstream endpoint,
// allowing tests to mock the Cno server without live network traffic.
func NewClientWithURL(keyPool *tmdb.KeyPool, siteBase string) (*Client, error) {
	base, err := streambase.New(keyPool)
	if err != nil {
		return nil, fmt.Errorf("cno streambase: %w", err)
	}

	return &Client{
		base:       base,
		httpClient: httpclient.NewWithUserAgent(config.DesktopUserAgent),
		siteBase:   strings.TrimRight(siteBase, "/"),
	}, nil
}

// Alias implements provider.Presenter with the display name shown in the UI.
func (c *Client) Alias() string { return "Cno" }

// Name implements provider.Provider with the canonical provider identifier.
func (c *Client) Name() string { return "cno" }

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

	sources := provider.FilterDirectSources(all)
	if len(sources) == 0 {
		if err != nil {
			return nil, err
		}
		return nil, provider.ErrNoSources
	}
	return sources, nil
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
