// Package vidora implements provider.Provider for Vidora
// (https://vidora.ac), a TMDB-keyed streaming embed player for movies, TV
// series, and animation.
//
// Catalog search and episode listings reuse the shared TMDB-keyed streambase.
// Playback fans out over the player's own source lanes and returns directly
// playable addresses: the Aphrodite content edge, the Shegu resolver, the
// Vidbox and Prism (mbox) players on stream.hls.lol, and Bandit's listing.
// Lanes that only expose player-internal handles (Moscow's ms_ token and
// Lightspeed's session-bound source) or that proxy through the separate
// resolve/dash/it endpoints are not queried: they cannot produce an address
// mpv can open on its own.
//
// Every lane and subtitle request carries the Vidora site origin as its
// Referer; the hls.lol hosts reject bare requests without it.
package vidora

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"kari/internal/config"
	"kari/internal/httpclient"
	"kari/internal/provider"
	"kari/internal/provider/streambase"
	"kari/internal/timeouts"
	"kari/internal/tmdb"
)

// providerID is the stable registry identifier.
const providerID = "vidora"

var (
	_ provider.Provider      = (*Client)(nil)
	_ provider.EpisodeSource = (*Client)(nil)
	_ provider.Presenter     = (*Client)(nil)
)

// Endpoints groups Vidora's file and resolution origins. The site origin is
// only used as the hotlink Referer; every playable address comes from one of
// the lane origins.
type Endpoints struct {
	Site    string
	Content string
	Stream  string
	Shegu   string
	Pal     string
	Subs    string
}

// Client implements provider.Provider against Vidora's catalog and lanes.
type Client struct {
	base       *streambase.Base
	httpClient *http.Client
	endpoints  Endpoints
	// laneBudget bounds one lane request so a stuck upstream cannot hold
	// the whole resolve to the service deadline.
	laneBudget time.Duration
}

// NewClient constructs the Vidora provider over the shared TMDB search base
// and the production lane origins.
func NewClient(keyPool *tmdb.KeyPool) (*Client, error) {
	return NewClientWithEndpoints(keyPool, Endpoints{
		Site:    config.VidoraBase,
		Content: config.VidoraContentBase,
		Stream:  config.VidoraStreamBase,
		Shegu:   config.VidoraSheguBase,
		Pal:     config.VidoraPalBase,
		Subs:    config.VidoraSubsBase,
	})
}

// NewClientWithEndpoints constructs the provider against explicit origins so
// offline tests can serve canned lane payloads.
func NewClientWithEndpoints(keyPool *tmdb.KeyPool, endpoints Endpoints) (*Client, error) {
	endpoints = trimEndpoints(endpoints)
	if err := endpoints.validate(); err != nil {
		return nil, err
	}
	base, err := streambase.New(keyPool)
	if err != nil {
		return nil, fmt.Errorf("vidora streambase: %w", err)
	}
	return &Client{
		base:       base,
		httpClient: httpclient.NewWithUserAgent(config.DesktopUserAgent),
		endpoints:  endpoints,
		laneBudget: timeouts.LaneBudget,
	}, nil
}

// trimEndpoints removes trailing slashes so path concatenation stays stable.
func trimEndpoints(endpoints Endpoints) Endpoints {
	return Endpoints{
		Site:    strings.TrimRight(strings.TrimSpace(endpoints.Site), "/"),
		Content: strings.TrimRight(strings.TrimSpace(endpoints.Content), "/"),
		Stream:  strings.TrimRight(strings.TrimSpace(endpoints.Stream), "/"),
		Shegu:   strings.TrimRight(strings.TrimSpace(endpoints.Shegu), "/"),
		Pal:     strings.TrimRight(strings.TrimSpace(endpoints.Pal), "/"),
		Subs:    strings.TrimRight(strings.TrimSpace(endpoints.Subs), "/"),
	}
}

// validate reports which origin is missing so construction never produces a
// client whose lanes silently point at relative URLs.
func (e Endpoints) validate() error {
	required := []struct {
		name  string
		value string
	}{
		{"site", e.Site},
		{"content", e.Content},
		{"stream", e.Stream},
		{"shegu", e.Shegu},
		{"pal", e.Pal},
		{"subs", e.Subs},
	}
	for _, endpoint := range required {
		if endpoint.value == "" {
			return fmt.Errorf("vidora: %s endpoint is required", endpoint.name)
		}
	}
	return nil
}

// Alias implements provider.Presenter with the display name shown in the UI.
func (c *Client) Alias() string { return "Vidora" }

// Name implements provider.Provider with the canonical provider identifier.
func (c *Client) Name() string { return providerID }

// Modes implements provider.Provider, declaring movies, TV series, and
// cartoon/animation modes.
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

// FetchEpisodes delegates episode and season lookups to the shared
// TMDB-backed streambase.
func (c *Client) FetchEpisodes(ctx context.Context, series provider.SearchResult) ([]provider.Episode, error) {
	return c.base.FetchEpisodes(ctx, series)
}
