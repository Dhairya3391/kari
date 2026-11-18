// Package zlive implements provider.Provider for zlive.st (https://zlive.st),
// a live sports and TV channel directory. The channel catalog is public JSON;
// playback URLs come from the site's signed /resolve endpoint, which this
// package reproduces natively so mpv can open the lanes directly.
package zlive

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"kari/internal/config"
	"kari/internal/httpclient"
	"kari/internal/logging"
	"kari/internal/provider"
	"kari/internal/timeouts"
)

var (
	_ provider.Provider           = (*Client)(nil)
	_ provider.EpisodeSource      = (*Client)(nil)
	_ provider.FeatureSource      = (*Client)(nil)
	_ provider.Presenter          = (*Client)(nil)
	_ provider.LiveStreamVerifier = (*Client)(nil)
)

// zliveLog scopes every line from this package with its identity.
var zliveLog = logging.With("provider", "zlive")

const (
	// handlePrefix marks zlive channel handles in search results and history.
	handlePrefix = "zlive:"
	// catalogTTL bounds how long the public channel catalog is reused. The
	// lineup changes a few times a day, so a short TTL only adds requests.
	catalogTTL = 15 * time.Minute
	// catalogFetchTimeout bounds one catalog refresh independently of the
	// caller's search budget.
	catalogFetchTimeout = timeouts.HTTPDefault
	// catalogAttemptTimeout bounds one catalog attempt so a stalled edge can
	// be retried within the overall refresh budget.
	catalogAttemptTimeout = timeouts.LiveMirror
	// catalogAttempts is the number of catalog attempts per refresh. The
	// upstream resets idle connections under load.
	catalogAttempts = 3
	// resolveTimeout bounds a ticket fetch; a signed resolution gets the
	// provider lane budget because it also fetches a fresh ticket.
	resolveTimeout = timeouts.LiveVerify
	// resolveRequestTimeout bounds the signed /resolve round trip.
	resolveRequestTimeout = timeouts.LaneBudget
	// resolveAttempts is the number of times a transient ticket or resolve
	// failure is retried; the upstream resets connections under load.
	resolveAttempts = 3
)

// Client implements provider.Provider against the zlive.st catalog and its
// signed stream resolver.
type Client struct {
	httpClient *http.Client
	apiBase    string
	siteBase   string

	mu        sync.Mutex
	catalog   []channel
	fetchedAt time.Time
}

// NewClient constructs the zlive provider against the production endpoints.
func NewClient() (*Client, error) {
	return NewClientWithURLs(config.ZliveAPIBase, config.ZliveSiteBase)
}

// NewClientWithURLs constructs the provider with custom upstream endpoints so
// offline tests can mock the catalog and resolution API.
func NewClientWithURLs(apiBase, siteBase string) (*Client, error) {
	apiBase = strings.TrimRight(strings.TrimSpace(apiBase), "/")
	siteBase = strings.TrimRight(strings.TrimSpace(siteBase), "/")
	for name, raw := range map[string]string{"api": apiBase, "site": siteBase} {
		parsed, err := url.Parse(raw)
		if err != nil || parsed.Scheme == "" || parsed.Host == "" {
			return nil, fmt.Errorf("zlive client: invalid %s URL %q", name, raw)
		}
	}
	return &Client{
		httpClient: httpclient.NewWithUserAgent(config.DesktopUserAgent),
		apiBase:    apiBase,
		siteBase:   siteBase,
	}, nil
}

// Name implements provider.Provider with the canonical provider identifier.
func (c *Client) Name() string { return "zlive" }

// Alias implements provider.Presenter with the display name shown in the UI.
func (c *Client) Alias() string { return "zlive" }

// Modes implements provider.Provider. Zlive trails the scheduled-event and
// live-TV directories so those rows stay ahead of this always-on catalog.
func (c *Client) Modes() []provider.Mode {
	return []provider.Mode{{Name: provider.ModeLive, Priority: 3}}
}

// Features implements provider.FeatureSource. The catalog is browsable with
// an empty query and changes often, so search results are never cached.
func (c *Client) Features(mode provider.ContentType) provider.Features {
	if mode != provider.ModeLive {
		return provider.Features{}
	}
	return provider.Features{
		AllowEmptyQuery:   true,
		NoCachedSearches:  true,
		SearchPlaceholder: "Search zlive channels…",
	}
}

// VerifyLiveStream implements provider.LiveStreamVerifier. Resolved lanes are
// short-lived, so the service probes them before handing them to the player.
func (c *Client) VerifyLiveStream(ctx context.Context, mode provider.ContentType, source provider.MediaSource) error {
	if mode != provider.ModeLive {
		return nil
	}
	return provider.VerifyLiveStreamWithHeaders(ctx, c.httpClient, source, mediaHeaders())
}
