// Package lightflix implements provider.Provider for Lightflix
// (https://lightflix.app), a TMDB-keyed movie and TV streaming site.
//
// Catalog search and episode listings reuse the shared TMDB-backed streambase.
// Playback is resolved through Lightflix's own tokenized, proof-of-work gated
// stream API, which yields a signed HLS master playable directly by mpv.
package lightflix

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"kari/internal/config"
	"kari/internal/httpclient"
	"kari/internal/provider"
	"kari/internal/provider/streambase"
	"kari/internal/tmdb"
)

const (
	providerID    = "lightflix"
	providerAlias = "Lightflix"
	// siteCookieName is the anti-bot cookie every playback endpoint requires.
	siteCookieName = "_lightflix_site"
	// tokenExpiryMargin retires cached session tokens before their hard expiry
	// so an in-flight resolve never carries one that expires mid-chain.
	tokenExpiryMargin = time.Minute
)

var (
	_ provider.Provider      = (*Client)(nil)
	_ provider.EpisodeSource = (*Client)(nil)
	_ provider.Presenter     = (*Client)(nil)
)

// Client implements provider.Provider against the Lightflix catalog and stream API.
type Client struct {
	base       *streambase.Base
	httpClient *http.Client
	siteBase   string

	mu                sync.Mutex
	siteCookie        string
	requestToken      string
	requestTokenUntil time.Time
}

// NewClient constructs the Lightflix provider with the production site origin.
func NewClient(keyPool *tmdb.KeyPool) (*Client, error) {
	return NewClientWithBase(keyPool, config.LightflixBase)
}

// NewClientWithBase constructs the Lightflix provider with a custom site origin
// so tests can exercise the resolution chain against a local server.
func NewClientWithBase(keyPool *tmdb.KeyPool, siteBase string) (*Client, error) {
	base, err := streambase.New(keyPool)
	if err != nil {
		return nil, fmt.Errorf("lightflix streambase: %w", err)
	}
	trimmed := strings.TrimRight(strings.TrimSpace(siteBase), "/")
	parsed, err := url.Parse(trimmed)
	if err != nil {
		return nil, fmt.Errorf("lightflix base URL: %w", err)
	}
	if parsed.Scheme == "" || parsed.Host == "" {
		return nil, fmt.Errorf("lightflix base URL must be absolute: %q", siteBase)
	}
	return &Client{
		base:       base,
		httpClient: httpclient.NewWithUserAgent(config.DesktopUserAgent),
		siteBase:   trimmed,
	}, nil
}

// Alias implements provider.Presenter with the display name shown in the UI.
func (c *Client) Alias() string { return providerAlias }

// Name implements provider.Provider with the canonical provider identifier.
func (c *Client) Name() string { return providerID }

// Modes implements provider.Provider, declaring the TMDB-keyed content modes
// Lightflix serves. Anime is deliberately absent: it is a TMDB discovery filter
// here rather than an AniList-keyed catalog shared with the anime providers.
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

// siteSessionCookie fetches the site origin once and caches the anti-bot cookie
// that all playback endpoints require.
func (c *Client) siteSessionCookie(ctx context.Context) (string, error) {
	c.mu.Lock()
	cached := c.siteCookie
	c.mu.Unlock()
	if cached != "" {
		return cached, nil
	}

	target := c.siteBase + "/en"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return "", fmt.Errorf("lightflix site request: %w", err)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("lightflix site request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= http.StatusBadRequest {
		return "", &provider.HTTPError{Code: resp.StatusCode, URL: target}
	}
	for _, cookie := range resp.Cookies() {
		if cookie.Name == siteCookieName && cookie.Value != "" {
			c.mu.Lock()
			c.siteCookie = cookie.Value
			c.mu.Unlock()
			return cookie.Value, nil
		}
	}
	return "", fmt.Errorf("lightflix site response missing %s cookie", siteCookieName)
}

// getRequestToken returns the page request token, cached until shortly before
// its upstream expiry. The token authorizes any title, so one per client is enough.
func (c *Client) getRequestToken(ctx context.Context, watchPath string) (string, error) {
	c.mu.Lock()
	cached, until := c.requestToken, c.requestTokenUntil
	c.mu.Unlock()
	if cached != "" && time.Now().Before(until) {
		return cached, nil
	}

	cookie, err := c.siteSessionCookie(ctx)
	if err != nil {
		return "", err
	}
	var out sessionResponse
	if err := c.postJSON(ctx, "/api/ui/session", map[string]string{"path": watchPath}, cookie, &out); err != nil {
		return "", fmt.Errorf("lightflix session: %w", err)
	}
	if out.Token == "" {
		return "", fmt.Errorf("lightflix session: empty token")
	}
	expiry := time.Now().Add(tokenExpiryMargin)
	if out.ExpiresAt > 0 {
		expiry = time.UnixMilli(out.ExpiresAt).Add(-tokenExpiryMargin)
	}
	c.mu.Lock()
	c.requestToken = out.Token
	c.requestTokenUntil = expiry
	c.mu.Unlock()
	return out.Token, nil
}

// resetSession drops cached credentials after an upstream rejects them so the
// next attempt obtains a fresh site cookie and request token.
func (c *Client) resetSession() {
	c.mu.Lock()
	c.siteCookie = ""
	c.requestToken = ""
	c.requestTokenUntil = time.Time{}
	c.mu.Unlock()
}

// postJSON sends a browser-shaped JSON POST to a Lightflix API path and decodes
// the response into out. Non-2xx responses become typed HTTP errors.
func (c *Client) postJSON(ctx context.Context, path string, body any, cookie string, out any) error {
	payload, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("lightflix encode %s: %w", path, err)
	}
	target := c.siteBase + path
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("lightflix request %s: %w", path, err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", config.DesktopUserAgent)
	req.Header.Set("Origin", c.siteBase)
	req.Header.Set("Referer", c.siteBase+"/en/")
	if cookie != "" {
		req.Header.Set("Cookie", siteCookieName+"="+cookie)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("lightflix request %s: %w", path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= http.StatusBadRequest {
		return &provider.HTTPError{Code: resp.StatusCode, URL: target}
	}
	raw, err := httpclient.ReadCapped(resp)
	if err != nil {
		return fmt.Errorf("lightflix read %s: %w", path, err)
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("lightflix decode %s: %w", path, err)
	}
	return nil
}
