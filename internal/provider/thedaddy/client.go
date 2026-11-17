// Package thedaddy implements provider.Provider for the DaddyLive sports and
// live TV directory (https://thedaddy.li). The guide is scraped from the
// site's schedule and 24/7 channel pages, and playback is resolved natively by
// walking each player page to its embed and extracting a direct HLS URL. No
// external service is involved: everything runs inside Kari, which matters
// because the CDNs behind the players reject datacenter IP ranges.
package thedaddy

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"kari/internal/config"
	"kari/internal/httpclient"
	"kari/internal/logging"
	"kari/internal/provider"
)

var (
	_ provider.Provider           = (*Client)(nil)
	_ provider.EpisodeSource      = (*Client)(nil)
	_ provider.FeatureSource      = (*Client)(nil)
	_ provider.Presenter          = (*Client)(nil)
	_ provider.LiveStreamVerifier = (*Client)(nil)
)

// thedaddyLog scopes every line from this package with its identity.
var thedaddyLog = logging.With("provider", "thedaddy")

const (
	// channelHandlePrefix marks 24/7 channel rows. Events use eventHandlePrefix
	// so a channel and a scheduled event that shares its channel id never
	// deduplicate against each other in the merged result list.
	channelHandlePrefix = "td:ch:"
	eventHandlePrefix   = "td:ev:"
	// guideTimeout bounds one full scrape of the schedule and channel pages.
	guideTimeout = 45 * time.Second
	// guideTTL is how long a scraped guide is reused. The site resets
	// connections under rapid requests, so browsing must not refetch per
	// keystroke.
	guideTTL = 3 * time.Minute
)

// Client implements provider.Provider by scraping thedaddy.li.
type Client struct {
	httpClient *http.Client
	baseURL    string

	guideMu sync.Mutex
	guide   *liveGuide
	guideAt time.Time
}

// NewClient constructs the provider against the given site base URL.
func NewClient(baseURL string) (*Client, error) {
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if base == "" {
		return nil, fmt.Errorf("thedaddy: empty base URL")
	}
	if parsed, err := url.Parse(base); err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return nil, fmt.Errorf("thedaddy: invalid base URL %q", baseURL)
	}
	return &Client{
		httpClient: httpclient.NewWithUserAgentAndTimeout(config.DesktopUserAgent, guideTimeout),
		baseURL:    base,
	}, nil
}

// Name implements provider.Provider.
func (c *Client) Name() string { return "thedaddy" }

// Alias implements provider.Presenter.
func (c *Client) Alias() string { return "TheDaddy" }

// Modes implements provider.Provider. DaddyLive leads the live tab: it serves
// scheduled sports events and 24/7 channels, unlike the live-TV-only catalogs
// that trail it.
func (c *Client) Modes() []provider.Mode {
	return []provider.Mode{{Name: provider.ModeLive, Priority: 1}}
}

// Features implements provider.FeatureSource.
func (c *Client) Features(mode provider.ContentType) provider.Features {
	if mode != provider.ModeLive {
		return provider.Features{}
	}
	return provider.Features{
		AllowEmptyQuery:   true,
		NoCachedSearches:  true,
		SearchPlaceholder: "Search live sports & channels…",
	}
}

// Search maps the guide's events and channels into live results, filtering on
// the query locally so keystrokes never hit the site.
func (c *Client) Search(ctx context.Context, query string, mode provider.ContentType) ([]provider.SearchResult, error) {
	if mode != provider.ModeLive {
		return nil, provider.ErrNoResults
	}
	guide, err := c.guideFor(ctx)
	if err != nil {
		return nil, fmt.Errorf("thedaddy search: %w", err)
	}
	for _, warning := range guide.Warnings {
		thedaddyLog.Debug("guide returned a partial warning", "warning", warning)
	}

	needle := strings.ToLower(strings.TrimSpace(query))
	results := make([]provider.SearchResult, 0, len(guide.Events)+len(guide.Channels))
	for _, event := range guide.Events {
		if len(event.Channels) == 0 {
			continue
		}
		if needle != "" && !eventMatches(event, needle) {
			continue
		}
		results = append(results, eventResult(event))
	}
	for _, channel := range guide.Channels {
		if channel.ID <= 0 || strings.TrimSpace(channel.Name) == "" {
			continue
		}
		if needle != "" && !strings.Contains(strings.ToLower(channel.Name), needle) {
			continue
		}
		results = append(results, provider.SearchResult{
			Title:     channel.Name,
			ID:        channelHandle(channel.ID),
			Type:      provider.ModeLive,
			MediaType: provider.MediaTypeLive,
			Live:      true,
			Group:     "Channel",
		})
	}
	if len(results) == 0 {
		return nil, provider.ErrNoResults
	}
	return results, nil
}

// FetchEpisodes returns the single playable unit a live stream has.
func (c *Client) FetchEpisodes(_ context.Context, series provider.SearchResult) ([]provider.Episode, error) {
	if strings.TrimSpace(series.ID) == "" {
		return nil, provider.ErrNoEpisodes
	}
	return []provider.Episode{{Title: series.Title, ID: series.ID}}, nil
}

// ResolveSource walks the player pages behind an event or channel until a lane
// actually plays.
func (c *Client) ResolveSource(ctx context.Context, mediaID string, episode provider.Episode) ([]provider.MediaSource, error) {
	handle := mediaID
	if strings.TrimSpace(handle) == "" {
		handle = episode.ID
	}
	channelIDs := parseChannelIDs(handle)
	if len(channelIDs) == 0 {
		return nil, fmt.Errorf("thedaddy resolve: invalid handle %q: %w", handle, provider.ErrNoSources)
	}
	sources, err := c.resolveLanes(ctx, channelIDs)
	if err != nil {
		return nil, fmt.Errorf("thedaddy resolve %q: %w", handle, err)
	}
	return sources, nil
}

// VerifyLiveStream implements provider.LiveStreamVerifier.
func (c *Client) VerifyLiveStream(ctx context.Context, mode provider.ContentType, source provider.MediaSource) error {
	if mode != provider.ModeLive {
		return nil
	}
	return provider.VerifyLiveStream(ctx, c.httpClient, source)
}

// guideFor returns a cached guide, scraping when it is older than guideTTL. A
// failed refresh falls back to the stale guide rather than an empty list.
func (c *Client) guideFor(ctx context.Context) (*liveGuide, error) {
	c.guideMu.Lock()
	defer c.guideMu.Unlock()

	if c.guide != nil && time.Since(c.guideAt) < guideTTL {
		return c.guide, nil
	}
	scrapeCtx, cancel := context.WithTimeout(ctx, guideTimeout)
	defer cancel()
	guide, err := c.scrapeGuide(scrapeCtx)
	if err != nil {
		if c.guide != nil {
			thedaddyLog.Warn("using stale guide after refresh failure", "error", err)
			return c.guide, nil
		}
		return nil, err
	}
	c.guide = guide
	c.guideAt = time.Now()
	return guide, nil
}

// eventResult maps one scheduled event to a live search result.
func eventResult(event guideEvent) provider.SearchResult {
	result := provider.SearchResult{
		Title:     event.Title,
		ID:        eventHandle(event.Channels),
		Type:      provider.ModeLive,
		MediaType: provider.MediaTypeLive,
		Live:      event.Live,
		Group:     event.Category,
	}
	if result.Title == "" {
		result.Title = "Live event"
	}
	if event.Category != "" {
		result.Genres = append(result.Genres, event.Category)
	}
	if event.Section != "" && event.Section != event.Category {
		result.Genres = append(result.Genres, event.Section)
	}
	if !event.StartsAt.IsZero() {
		result.StartsAt = event.StartsAt.Local()
	}
	return result
}

// eventMatches reports whether the event's title, buckets or channels contain
// the lowercased needle.
func eventMatches(event guideEvent, needle string) bool {
	if strings.Contains(strings.ToLower(event.Title), needle) ||
		strings.Contains(strings.ToLower(event.Category), needle) ||
		strings.Contains(strings.ToLower(event.Section), needle) {
		return true
	}
	for _, channel := range event.Channels {
		if strings.Contains(strings.ToLower(channel.Name), needle) {
			return true
		}
	}
	return false
}

// channelHandle builds the provider handle for a 24/7 channel.
func channelHandle(id int) string {
	return fmt.Sprintf("%s%d", channelHandlePrefix, id)
}

// eventHandle builds the provider handle for an event carried by one or more
// channels. Encoding the channel ids keeps resolution independent of any
// cached guide snapshot.
func eventHandle(channels []guideChannel) string {
	seen := make(map[int]struct{}, len(channels))
	ids := make([]string, 0, len(channels))
	for _, channel := range channels {
		if channel.ID <= 0 {
			continue
		}
		if _, dup := seen[channel.ID]; dup {
			continue
		}
		seen[channel.ID] = struct{}{}
		ids = append(ids, strconv.Itoa(channel.ID))
	}
	return eventHandlePrefix + strings.Join(ids, "+")
}

// parseChannelIDs extracts every numeric channel id from a provider handle.
func parseChannelIDs(handle string) []int {
	fields := strings.FieldsFunc(handle, func(r rune) bool {
		return r < '0' || r > '9'
	})
	ids := make([]int, 0, len(fields))
	seen := make(map[int]struct{}, len(fields))
	for _, field := range fields {
		id, err := strconv.Atoi(field)
		if err != nil || id <= 0 {
			continue
		}
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	return ids
}
