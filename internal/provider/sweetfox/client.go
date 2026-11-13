// Package sweetfox implements provider.Provider for SweetFox
// (https://sweetfox.app), a TMDB-keyed streaming site for movies, television
// series, and animated content.
//
// Catalog search and episode listings reuse the shared TMDB-keyed streambase.
// Playback resolves through the VidQueen player SweetFox embeds
// (https://player.vidqueen.st). VidQueen exposes seven stream lanes —
// Bloodstone, Obsidian, Garnet, Ruby, Jet, Amethyst, Nightshade — and
// ResolveStream queries every one of them so the UI sees all servers and
// qualities, including 4K when a lane provides it.
//
// Every returned stream is directly playable by mpv; header-gated CDNs are
// covered by the Referer/Origin values attached to each source.
package sweetfox

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

// Client implements provider.Provider against SweetFox and the VidQueen
// resolution API.
type Client struct {
	base       *streambase.Base
	httpClient *http.Client
	apiBase    string
	playerBase string
}

// NewClient constructs the SweetFox provider with the shared TMDB search base
// and the production VidQueen endpoints.
func NewClient(keyPool *tmdb.KeyPool) (*Client, error) {
	return NewClientWithURLs(keyPool, config.SweetFoxAPIBase, config.SweetFoxPlayerBase)
}

// NewClientWithURLs constructs the SweetFox provider with custom upstream
// endpoints, allowing offline tests to mock the VidQueen API and player without
// network access.
func NewClientWithURLs(keyPool *tmdb.KeyPool, apiBase, playerBase string) (*Client, error) {
	base, err := streambase.New(keyPool)
	if err != nil {
		return nil, fmt.Errorf("sweetfox streambase: %w", err)
	}
	return &Client{
		base:       base,
		httpClient: httpclient.NewWithUserAgent(config.DesktopUserAgent),
		apiBase:    strings.TrimRight(apiBase, "/"),
		playerBase: strings.TrimRight(playerBase, "/"),
	}, nil
}

// Alias implements provider.Presenter with the display name shown in the UI.
func (c *Client) Alias() string { return "SweetFox" }

// Name implements provider.Provider with the canonical provider identifier.
func (c *Client) Name() string { return "sweetfox" }

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

	sources := all
	if len(sources) == 0 {
		if err != nil {
			return nil, err
		}
		return nil, provider.ErrNoSources
	}
	return sources, nil
}

// ResolveStream implements provider.StreamingProvider, pushing playable sources
// to updates once every VidQueen lane has answered.
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
