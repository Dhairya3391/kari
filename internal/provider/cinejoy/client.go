// Package cinejoy implements provider.Provider for Cinejoy
// (https://cinejoy.pk), a TMDB-keyed streaming site for movies, television
// series, and animated content.
//
// Catalog search and episode listings reuse the shared TMDB-keyed streambase.
// Playback resolves through Cinejoy's sealed gateway (https://api2.wing.st),
// which advertises several stream lanes (Light, Nebula, Sirius, Polaris). Every
// lane is queried so the UI sees all servers, their adaptive HLS masters, and
// the individual quality tiers including 4K when a lane provides it.
//
// Every returned stream is directly playable by mpv. The lane CDNs require the
// Cinejoy origin, which the attached Referer supplies to the player.
package cinejoy

import (
	"context"
	"crypto/ecdh"
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

// Client implements provider.Provider against the Cinejoy catalog and its
// sealed resolution gateway.
type Client struct {
	base       *streambase.Base
	httpClient *http.Client
	gate       *ecdh.PublicKey
	apiBase    string
	siteBase   string
}

// NewClient constructs the Cinejoy provider with the shared TMDB search base
// and the production gateway endpoints.
func NewClient(keyPool *tmdb.KeyPool) (*Client, error) {
	return NewClientWithURLs(keyPool, config.CinejoyAPIBase, config.CinejoyBase)
}

// NewClientWithURLs constructs the Cinejoy provider with custom upstream
// endpoints, allowing offline tests to mock the gateway and site origin.
func NewClientWithURLs(keyPool *tmdb.KeyPool, apiBase, siteBase string) (*Client, error) {
	gate, err := parseGateKey()
	if err != nil {
		return nil, err
	}
	return newClientWithGate(keyPool, apiBase, siteBase, gate)
}

// newClientWithGate constructs the provider with an explicit gateway public
// key so tests can run a real sealing/opening round trip against a local key.
func newClientWithGate(keyPool *tmdb.KeyPool, apiBase, siteBase string, gate *ecdh.PublicKey) (*Client, error) {
	base, err := streambase.New(keyPool)
	if err != nil {
		return nil, fmt.Errorf("cinejoy streambase: %w", err)
	}
	return &Client{
		base:       base,
		httpClient: httpclient.NewWithUserAgent(config.DesktopUserAgent),
		gate:       gate,
		apiBase:    strings.TrimRight(apiBase, "/"),
		siteBase:   strings.TrimRight(siteBase, "/"),
	}, nil
}

// Alias implements provider.Presenter with the display name shown in the UI.
func (c *Client) Alias() string { return "Cinejoy" }

// Name implements provider.Provider with the canonical provider identifier.
func (c *Client) Name() string { return "cinejoy" }

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

// ResolveSource resolves playable media sources synchronously by draining the
// per-lane batches the streaming fan-out produces.
func (c *Client) ResolveSource(ctx context.Context, mediaID string, episode provider.Episode) ([]provider.MediaSource, error) {
	tmdbID, mediaType, season, epNum, err := parseCoordinates(mediaID, episode)
	if err != nil {
		return nil, err
	}

	ch := make(chan []provider.MediaSource, 4)
	errCh := make(chan error, 1)
	var all []provider.MediaSource

	go func() {
		errCh <- c.fetchSources(ctx, tmdbID, mediaType, season, epNum, ch)
		close(ch)
	}()

	for batch := range ch {
		all = append(all, batch...)
	}
	err = <-errCh

	if len(all) == 0 {
		if err != nil {
			return nil, err
		}
		return nil, provider.ErrNoSources
	}
	return all, nil
}

// ResolveStream implements provider.StreamingProvider, pushing each lane's
// playable rows as soon as that lane answers so playback can start on the
// first server to respond.
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
