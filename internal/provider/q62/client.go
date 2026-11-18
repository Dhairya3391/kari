// Package q62 implements provider.Provider for Q62 (https://q62flix.ws), a
// catalog of movies and TV shows whose metadata is sourced from TMDB but whose
// content is keyed by opaque site IDs.
//
// Search and episode listings use the site's own JSON API because Q62 offers
// no TMDB-ID lookup; the resulting site IDs flow through to resolution. A
// lazily built TMDB-ID index lets resolution also work when another provider
// supplied the search result. Signed release descriptors point at relative
// HLS and WebVTT paths served only by Q62's media proxy.
package q62

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"

	"kari/internal/config"
	"kari/internal/httpclient"
	"kari/internal/provider"
)

var (
	_ provider.Provider          = (*Client)(nil)
	_ provider.EpisodeSource     = (*Client)(nil)
	_ provider.StreamingProvider = (*Client)(nil)
	_ provider.Presenter         = (*Client)(nil)
)

// Client implements provider.Provider against Q62's catalog and stream proxy.
type Client struct {
	httpClient *http.Client
	apiBase    string
	siteBase   string
	proxyBase  string

	indexMu sync.Mutex
	index   *tmdbIndex
}

// NewClient constructs the Q62 provider with the production endpoints.
func NewClient() (*Client, error) {
	return NewClientWithURLs(config.Q62APIBase, config.Q62Base, config.Q62StreamProxyBase)
}

// NewClientWithURLs constructs the Q62 provider with custom upstream endpoints
// so offline tests can mock the catalog API and media proxy.
func NewClientWithURLs(apiBase, siteBase, proxyBase string) (*Client, error) {
	apiBase = strings.TrimRight(strings.TrimSpace(apiBase), "/")
	siteBase = strings.TrimRight(strings.TrimSpace(siteBase), "/")
	proxyBase = strings.TrimRight(strings.TrimSpace(proxyBase), "/")
	if apiBase == "" || siteBase == "" || proxyBase == "" {
		return nil, fmt.Errorf("q62 client: api, site, and proxy URLs are required")
	}
	return &Client{
		httpClient: httpclient.NewWithUserAgent(config.DesktopUserAgent),
		apiBase:    apiBase,
		siteBase:   siteBase,
		proxyBase:  proxyBase,
	}, nil
}

// Alias implements provider.Presenter with the display name shown in the UI.
func (c *Client) Alias() string { return "Q62" }

// Name implements provider.Provider with the canonical provider identifier.
func (c *Client) Name() string { return "q62" }

// Modes implements provider.Provider, declaring support for movies and TV.
func (c *Client) Modes() []provider.Mode {
	return []provider.Mode{
		{Name: provider.ModeMovies, Priority: 2},
		{Name: provider.ModeTV, Priority: 2},
	}
}

// getJSON performs a catalog API GET and decodes the JSON body into target.
func getJSON[T any](ctx context.Context, c *Client, path string) (T, error) {
	var zero T
	target := c.apiBase + path
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return zero, fmt.Errorf("q62 request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return zero, fmt.Errorf("q62 request %s: %w", path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return zero, &provider.HTTPError{Code: resp.StatusCode, URL: target}
	}
	raw, err := httpclient.ReadCapped(resp)
	if err != nil {
		return zero, fmt.Errorf("q62 read %s: %w", path, err)
	}
	var payload T
	if err := json.Unmarshal(raw, &payload); err != nil {
		return zero, fmt.Errorf("q62 decode %s: %w", path, err)
	}
	return payload, nil
}
