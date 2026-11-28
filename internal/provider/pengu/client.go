// Package pengu implements provider.Provider for PenguPlay
// (https://pengu.uk), a TMDB-keyed aggregator that resolves movies, series
// and cartoons to signed direct streams.
//
// Catalog search and episode listings reuse the shared TMDB-backed
// streambase. Playback asks PenguPlay's own stream resource for the title's
// lanes, which arrive as Stremio-shaped stream objects even though Kari talks
// to the API directly rather than through an addon integration. The access
// token is part of the provider's upstream identity and is compiled in; it is
// deliberately not a user setting.
package pengu

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
	_ provider.Provider           = (*Client)(nil)
	_ provider.EpisodeSource      = (*Client)(nil)
	_ provider.Presenter          = (*Client)(nil)
	_ provider.FeatureSource      = (*Client)(nil)
	_ provider.LiveStreamVerifier = (*Client)(nil)
)

// Client implements provider.Provider against PenguPlay's catalog and stream
// resource.
type Client struct {
	base       *streambase.Base
	httpClient *http.Client
	apiBase    string
	live       liveCache
}

// NewClient constructs the PenguPlay provider with the bundled token.
func NewClient(keyPool *tmdb.KeyPool) (*Client, error) {
	return NewClientWithBase(keyPool, config.PenguBase, config.PenguToken)
}

// NewClientWithBase constructs the PenguPlay provider against an explicit
// host and token so offline tests can point it at a local server. An empty
// host or token is rejected: both are required to address the API.
func NewClientWithBase(keyPool *tmdb.KeyPool, base, token string) (*Client, error) {
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	token = strings.TrimSpace(token)
	if base == "" {
		return nil, fmt.Errorf("pengu: base URL is required")
	}
	if token == "" {
		return nil, fmt.Errorf("pengu: access token is required")
	}
	base2, err := streambase.New(keyPool)
	if err != nil {
		return nil, fmt.Errorf("pengu streambase: %w", err)
	}
	return &Client{
		base:       base2,
		httpClient: httpclient.New(),
		apiBase:    base + "/" + encodedAPIConfig(token),
	}, nil
}

// Alias implements provider.Presenter with the display name shown in the UI.
func (c *Client) Alias() string { return "PenguPlay" }

// Name implements provider.Provider with the canonical provider identifier.
func (c *Client) Name() string { return "pengu" }

// Modes implements provider.Provider. PenguPlay keys every lane by TMDB ID, so
// it serves movies, TV series and animated titles through the same resource;
// its live catalogs add sports and always-on channels.
func (c *Client) Modes() []provider.Mode {
	return []provider.Mode{
		{Name: provider.ModeMovies, Priority: 2},
		{Name: provider.ModeTV, Priority: 2},
		{Name: provider.ModeCartoon, Priority: 2},
		{Name: provider.ModeLive, Priority: 2},
	}
}

// Features implements provider.FeatureSource. Live mode is browsable without
// a query and its event list changes during the day, so the UI must not cache
// it between visits.
func (c *Client) Features(mode provider.ContentType) provider.Features {
	if mode != provider.ModeLive {
		return provider.Features{}
	}
	return provider.Features{
		AllowEmptyQuery:   true,
		NoCachedSearches:  true,
		SearchPlaceholder: "Search live sports, teams, or channels…",
	}
}

// Search delegates title discovery to the shared TMDB-backed streambase and
// serves live events from PenguPlay's own catalogs.
func (c *Client) Search(ctx context.Context, query string, mode provider.ContentType) ([]provider.SearchResult, error) {
	if mode == provider.ModeLive {
		return c.searchLive(ctx, query)
	}
	return c.base.Search(ctx, query, mode)
}

// FetchEpisodes delegates season and episode listings to the shared
// TMDB-backed streambase. A live event has exactly one playable unit.
func (c *Client) FetchEpisodes(ctx context.Context, series provider.SearchResult) ([]provider.Episode, error) {
	if isLiveHandle(series.ID) || series.Type == provider.ModeLive {
		return []provider.Episode{{Title: series.Title, ID: series.ID}}, nil
	}
	return c.base.FetchEpisodes(ctx, series)
}
