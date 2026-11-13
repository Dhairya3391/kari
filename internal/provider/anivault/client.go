// Package anivault resolves anime through AniVault's public catalog and
// non-Anikoto playback integrations. Catalog IDs are explicitly separated from
// AniList IDs, and signed playback URLs are resolved fresh for each request.
package anivault

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"kari/internal/config"
	"kari/internal/httpclient"
	"kari/internal/provider"
	"kari/internal/provider/kit"
)

const (
	providerID        = "anivault"
	sourceAnimeHeaven = "animeheaven"
)

// Client implements AniVault's catalog and incremental playback capabilities.
type Client struct {
	http            *http.Client
	base            string
	anilistEndpoint string
}

// NewClient constructs AniVault with the shared HTTP client.
func NewClient() (*Client, error) {
	return NewClientWithBase(config.AniVaultAPIBase)
}

// NewClientWithBase constructs AniVault against a custom website base URL.
func NewClientWithBase(baseURL string) (*Client, error) {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		return nil, fmt.Errorf("anivault: empty API base URL")
	}
	return &Client{
		http:            httpclient.New(),
		base:            baseURL,
		anilistEndpoint: config.AniListAPIBase,
	}, nil
}

// Alias returns the human-readable provider name.
func (c *Client) Alias() string { return "AniVault" }

// Name returns the stable registry identifier.
func (c *Client) Name() string { return providerID }

// Modes registers AniVault for anime.
func (c *Client) Modes() []provider.Mode {
	return []provider.Mode{{Name: provider.ModeAnime, Priority: 2}}
}

// RequiresEpisodeListForMovies keeps movies on the per-episode watch flow.
func (c *Client) RequiresEpisodeListForMovies() bool { return true }

// Features declares separate sub/dub resolution for anime.
func (c *Client) Features(mode provider.ContentType) provider.Features {
	return provider.Features{AudioSelection: mode == provider.ModeAnime}
}

func (c *Client) get(ctx context.Context, path string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+path, nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", config.DesktopUserAgent)
	req.Header.Set("Referer", c.base+"/")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch %s: %w", redactPath(path), err)
	}
	defer resp.Body.Close()
	body, err := httpclient.ReadCapped(resp)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	message := apiMessage(body)
	if resp.StatusCode == http.StatusOK {
		if message != "" {
			return nil, fmt.Errorf("%s: %s: %w", redactPath(path), message, provider.ErrNoSources)
		}
		return body, nil
	}
	httpErr := &provider.HTTPError{Code: resp.StatusCode, URL: redactPath(path)}
	switch resp.StatusCode {
	case http.StatusNotFound:
		return nil, fmt.Errorf("%s: %s: %w: %w", redactPath(path), message, provider.ErrNotFound, httpErr)
	case http.StatusForbidden:
		return nil, fmt.Errorf("%s: %w: %w", redactPath(path), provider.ErrBlocked, httpErr)
	default:
		return nil, fmt.Errorf("%s: %w", message, httpErr)
	}
}

func apiMessage(body []byte) string {
	var payload struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(body, &payload) != nil {
		return ""
	}
	message := kit.Clean(payload.Error)
	if len(message) > 200 {
		message = message[:200]
	}
	return message
}

func redactPath(path string) string {
	if i := strings.IndexByte(path, '?'); i >= 0 {
		return path[:i]
	}
	return path
}

type watchResponse struct {
	Server       string        `json:"server"`
	Source       string        `json:"source"`
	Servers      []apiServer   `json:"servers"`
	EmbedURL     string        `json:"embedUrl"`
	M3U8         string        `json:"m3u8"`
	HLSProxyURL  string        `json:"hlsProxyUrl"`
	MP4          string        `json:"mp4"`
	MP4ProxyURL  string        `json:"mp4ProxyUrl"`
	StreamURL    string        `json:"streamUrl"`
	RawStreamURL string        `json:"rawStreamUrl"`
	IframeOnly   bool          `json:"iframeOnly"`
	Subtitles    []apiSubtitle `json:"subtitles"`
}

type apiSubtitle struct {
	URL     string `json:"url"`
	Lang    string `json:"lang"`
	Default bool   `json:"default"`
}

var (
	_ provider.Provider          = (*Client)(nil)
	_ provider.Presenter         = (*Client)(nil)
	_ provider.FeatureSource     = (*Client)(nil)
	_ provider.MovieEpisodeFlow  = (*Client)(nil)
	_ provider.StreamingProvider = (*Client)(nil)
)
