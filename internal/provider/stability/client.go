// Package stability implements provider.Provider for the Stability live sports
// and channels platform (https://stability.taxidev.me). It surfaces scheduled
// sporting events and 24/7 channels in ModeLive and resolves direct HLS playback
// streams.
package stability

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"

	"kari/internal/config"
	"kari/internal/httpclient"
	"kari/internal/logging"
	"kari/internal/provider"
)

var (
	_ provider.Provider       = (*Client)(nil)
	_ provider.DeadlineSource = (*Client)(nil)
	_ provider.EpisodeSource  = (*Client)(nil)
	_ provider.FeatureSource  = (*Client)(nil)
	_ provider.Presenter      = (*Client)(nil)
)

var stabilityLog = logging.With("provider", "stability")

const (
	handlePrefix     = "stability:"
	scheduleTTL      = 60 * time.Second
	scheduleCacheKey = "schedule"
	fetchTimeout     = 15 * time.Second
)

type stabilityEvent struct {
	ID        string             `json:"id"`
	Title     string             `json:"title"`
	Slug      string             `json:"slug"`
	Date      string             `json:"date"`
	Time      string             `json:"time"`
	Timestamp int64              `json:"timestamp"`
	Category  string             `json:"category"`
	Team1     string             `json:"team1"`
	Team2     string             `json:"team2"`
	Poster    string             `json:"poster"`
	Channels  []stabilityChannel `json:"channels"`
	Sources   []stabilitySource  `json:"sources"`
	M3U8s     []string           `json:"m3u8s"`
}

type stabilityChannel struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Type     string `json:"type"`
	Language string `json:"language"`
	HID      string `json:"hid"`
	Priority int    `json:"priority"`
}

type stabilitySource struct {
	EventID  string `json:"eventId"`
	Name     string `json:"name"`
	URL      string `json:"url"`
	HID      string `json:"hid"`
	Type     string `json:"type"`
	Language string `json:"language"`
	Priority int    `json:"priority"`
}

type channelSearchResponse struct {
	Channels []channelSearchItem `json:"channels"`
	Total    int                 `json:"total"`
}

type channelSearchItem struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Icon string `json:"icon"`
}

type streamFetchResponse struct {
	Status  string            `json:"status"`
	URL     string            `json:"url"`
	Streams []streamFetchItem `json:"streams"`
}

type streamFetchItem struct {
	MediaURL string `json:"media_url"`
	URL      string `json:"url"`
}

type warmResponse struct {
	Status    string           `json:"status"`
	URL       string           `json:"url"`
	MasterURL string           `json:"master_url"`
	Streams   []warmStreamItem `json:"streams"`
}

type warmStreamItem struct {
	MediaURL string `json:"media_url"`
	URL      string `json:"url"`
}

// Client implements provider.Provider for Stability.
type Client struct {
	httpClient *http.Client
	siteBase   string
	apiBase    string
	sf         singleflight.Group
	mu         sync.Mutex
	schedule   []stabilityEvent
	fetchedAt  time.Time
}

// NewClient constructs the Stability provider against the default production site.
func NewClient() (*Client, error) {
	return NewClientWithURL(
		config.StabilityBase,
		httpclient.NewWithUserAgentAndTimeout(config.DesktopUserAgent, 20*time.Second),
	)
}

// NewClientWithURL constructs the provider with a custom site URL and HTTP client.
func NewClientWithURL(siteBase string, hc *http.Client) (*Client, error) {
	siteBase = strings.TrimRight(strings.TrimSpace(siteBase), "/")
	if siteBase == "" {
		return nil, fmt.Errorf("stability: empty site base URL")
	}
	u, err := url.Parse(siteBase)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return nil, fmt.Errorf("stability: invalid site base URL %q: %w", siteBase, err)
	}
	if hc == nil {
		hc = httpclient.NewWithUserAgentAndTimeout(config.DesktopUserAgent, 20*time.Second)
	}
	apiBase := siteBase + "/api"
	if strings.TrimRight(siteBase, "/") == strings.TrimRight(config.StabilityBase, "/") {
		apiBase = config.StabilityAPIBase
	}
	return &Client{
		httpClient: hc,
		siteBase:   siteBase,
		apiBase:    apiBase,
	}, nil
}

// Name implements provider.Provider.
func (c *Client) Name() string { return "stability" }

// Alias implements provider.Presenter.
func (c *Client) Alias() string { return "Stability" }

// Modes implements provider.Provider. Stability serves live sports and channels.
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
		SearchPlaceholder: "Search live sports or channels…",
	}
}

// Deadlines implements provider.DeadlineSource.
func (c *Client) Deadlines(mode provider.ContentType) provider.Deadlines {
	if mode == provider.ModeLive {
		return provider.Deadlines{Source: 40 * time.Second}
	}
	return provider.Deadlines{}
}

// Search queries Stability's schedule and channel catalog for matching entries.
func (c *Client) Search(ctx context.Context, query string, mode provider.ContentType) ([]provider.SearchResult, error) {
	if mode != provider.ModeLive {
		return nil, provider.ErrNoResults
	}

	needle := strings.ToLower(strings.TrimSpace(query))
	events, err := c.getSchedule(ctx)
	if err != nil && needle == "" {
		return nil, fmt.Errorf("stability search schedule: %w", err)
	}

	results := make([]provider.SearchResult, 0, len(events))
	now := time.Now()

	for _, ev := range events {
		if !matchesEvent(ev, needle) {
			continue
		}
		results = append(results, toEventResult(ev, now))
	}

	var chanErr error
	if needle != "" {
		channels, err2 := c.searchChannels(ctx, needle)
		if err2 == nil {
			for _, ch := range channels {
				results = append(results, toChannelResult(ch))
			}
		} else {
			chanErr = err2
			stabilityLog.Debug("channels search query failed", "err", chanErr)
		}
	}

	if len(results) == 0 {
		if err != nil && (needle == "" || chanErr != nil) {
			return nil, fmt.Errorf("stability search: schedule: %w, channels: %w", err, chanErr)
		}
		return nil, provider.ErrNoResults
	}
	return results, nil
}

// FetchEpisodes returns the single playable episode unit for a live stream result.
func (c *Client) FetchEpisodes(_ context.Context, series provider.SearchResult) ([]provider.Episode, error) {
	if strings.TrimSpace(series.ID) == "" {
		return nil, provider.ErrNoEpisodes
	}
	return []provider.Episode{{Title: series.Title, ID: series.ID}}, nil
}

// ResolveSource discovers and verifies playable HLS stream manifests for the target media item.
func (c *Client) ResolveSource(ctx context.Context, mediaID string, episode provider.Episode) ([]provider.MediaSource, error) {
	handle := mediaID
	if !strings.HasPrefix(handle, handlePrefix) && strings.HasPrefix(episode.ID, handlePrefix) {
		handle = episode.ID
	}
	kind, id := parseHandle(handle)
	if id == "" {
		return nil, fmt.Errorf("stability resolve: empty target ID: %w", provider.ErrNoSources)
	}

	var mu sync.Mutex
	seenURLs := make(map[string]bool)
	var sources []provider.MediaSource

	addSource := func(streamURL, name string) {
		streamURL = strings.TrimSpace(streamURL)
		if streamURL == "" {
			return
		}
		mu.Lock()
		defer mu.Unlock()
		if seenURLs[streamURL] {
			return
		}
		seenURLs[streamURL] = true
		sources = append(sources, provider.MediaSource{
			URL:       streamURL,
			Quality:   "Auto",
			Backend:   name,
			Type:      provider.SourceTypeHLS,
			Referer:   c.siteBase + "/",
			UserAgent: config.DesktopUserAgent,
		})
	}

	if kind == "event" {
		c.resolveEventSources(ctx, id, addSource)
	} else {
		c.resolveChannelSources(ctx, id, addSource)
	}

	if len(sources) == 0 {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		return nil, provider.ErrNoSources
	}
	return sources, nil
}

func (c *Client) resolveEventSources(ctx context.Context, eventID string, addSource func(string, string)) {
	events, _ := c.getSchedule(ctx)
	var matched *stabilityEvent
	for i := range events {
		if events[i].ID == eventID || events[i].Slug == eventID {
			matched = &events[i]
			break
		}
	}

	if matched != nil {
		type fetchJob struct {
			slug string
			name string
		}
		var jobs []fetchJob

		for _, s := range matched.Sources {
			name := s.Name
			if name == "" {
				name = "Stream"
			}
			if s.Language != "" {
				name = fmt.Sprintf("%s (%s)", name, s.Language)
			}
			if s.URL != "" {
				addSource(s.URL, name)
			}
			if s.HID != "" {
				addSource(fmt.Sprintf("%s/h/%s/playlist.m3u8", config.StabilityTaxiCDNBase, s.HID), name)
				addSource(fmt.Sprintf("%s/live/%s/master.m3u8", config.StabilityCloudCDNBase, s.HID), name)
				jobs = append(jobs, fetchJob{slug: s.HID, name: name})
			}
		}

		for _, ch := range matched.Channels {
			name := ch.Name
			if name == "" {
				name = "Channel"
			}
			hid := ch.HID
			if hid == "" {
				hid = ch.ID
			}
			if hid != "" {
				addSource(fmt.Sprintf("%s/h/%s/playlist.m3u8", config.StabilityTaxiCDNBase, hid), name)
				addSource(fmt.Sprintf("%s/live/%s/master.m3u8", config.StabilityCloudCDNBase, hid), name)
				jobs = append(jobs, fetchJob{slug: hid, name: name})
			}
		}

		for i, m3u8 := range matched.M3U8s {
			addSource(m3u8, fmt.Sprintf("Source %d", i+1))
		}

		if len(jobs) > 0 {
			var wg sync.WaitGroup
			sem := make(chan struct{}, 4)
			for _, j := range jobs {
				wg.Add(1)
				go func(job fetchJob) {
					defer wg.Done()
					select {
					case sem <- struct{}{}:
					case <-ctx.Done():
						return
					}
					defer func() { <-sem }()
					c.fetchStreamURLs(ctx, job.slug, job.name, addSource)
				}(j)
			}
			wg.Wait()
		}
	} else {
		c.fetchStreamURLs(ctx, eventID, "Stream", addSource)
	}
}

func (c *Client) resolveChannelSources(ctx context.Context, channelID string, addSource func(string, string)) {
	addSource(fmt.Sprintf("%s/h/%s/playlist.m3u8", config.StabilityTaxiCDNBase, channelID), "Channel")
	addSource(fmt.Sprintf("%s/live/%s/master.m3u8", config.StabilityCloudCDNBase, channelID), "Channel")

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		c.fetchStreamURLs(ctx, channelID, "Channel", addSource)
	}()
	go func() {
		defer wg.Done()
		c.warmStreamURLs(ctx, channelID, "Channel", addSource)
	}()
	wg.Wait()
}

func (c *Client) fetchStreamURLs(ctx context.Context, slug, name string, addSource func(string, string)) {
	endpoint := fmt.Sprintf("%s/fetch?slug=%s", c.apiBase, url.QueryEscape(slug))
	var resp streamFetchResponse
	if err := c.getJSON(ctx, endpoint, &resp); err != nil {
		stabilityLog.Debug("stream fetch endpoint failed", "slug", slug, "err", err)
		return
	}
	if resp.URL != "" {
		addSource(resp.URL, name)
	}
	for _, s := range resp.Streams {
		if s.MediaURL != "" {
			addSource(s.MediaURL, name)
		}
		if s.URL != "" {
			addSource(s.URL, name)
		}
	}
}

func (c *Client) warmStreamURLs(ctx context.Context, id, name string, addSource func(string, string)) {
	now := time.Now().Unix()
	end := now + 14400
	endpoint := fmt.Sprintf("%s/warm?id=%s&start=%d&end=%d&slug=%s", c.apiBase, url.QueryEscape(id), now, end, url.QueryEscape(id))
	var resp warmResponse
	if err := c.getJSON(ctx, endpoint, &resp); err != nil {
		stabilityLog.Debug("stream warm endpoint failed", "id", id, "err", err)
		return
	}
	if resp.MasterURL != "" {
		addSource(resp.MasterURL, name)
	}
	if resp.URL != "" {
		addSource(resp.URL, name)
	}
	for _, s := range resp.Streams {
		if s.MediaURL != "" {
			addSource(s.MediaURL, name)
		}
		if s.URL != "" {
			addSource(s.URL, name)
		}
	}
}

func (c *Client) getSchedule(ctx context.Context) ([]stabilityEvent, error) {
	c.mu.Lock()
	if len(c.schedule) > 0 && time.Since(c.fetchedAt) < scheduleTTL {
		cached := c.schedule
		c.mu.Unlock()
		return cached, nil
	}
	c.mu.Unlock()

	result := c.sf.DoChan(scheduleCacheKey, func() (any, error) {
		c.mu.Lock()
		if len(c.schedule) > 0 && time.Since(c.fetchedAt) < scheduleTTL {
			cached := c.schedule
			c.mu.Unlock()
			return cached, nil
		}
		c.mu.Unlock()

		fetchCtx, cancel := context.WithTimeout(context.Background(), fetchTimeout)
		defer cancel()

		var events []stabilityEvent
		endpoint := c.apiBase + "/schedule"
		if err := c.getJSON(fetchCtx, endpoint, &events); err != nil {
			c.mu.Lock()
			if len(c.schedule) > 0 {
				cached := c.schedule
				c.mu.Unlock()
				stabilityLog.Debug("schedule refresh failed; returning stale cache", "err", err)
				return cached, nil
			}
			c.mu.Unlock()
			return nil, err
		}

		c.mu.Lock()
		c.schedule = events
		c.fetchedAt = time.Now()
		c.mu.Unlock()
		return events, nil
	})

	select {
	case res := <-result:
		if res.Err != nil {
			return nil, res.Err
		}
		events, ok := res.Val.([]stabilityEvent)
		if !ok {
			return nil, errors.New("invalid schedule cache value")
		}
		return events, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (c *Client) searchChannels(ctx context.Context, query string) ([]channelSearchItem, error) {
	endpoint := fmt.Sprintf("%s/channels?q=%s&limit=50", c.apiBase, url.QueryEscape(query))
	var resp channelSearchResponse
	if err := c.getJSON(ctx, endpoint, &resp); err != nil {
		return nil, err
	}
	return resp.Channels, nil
}

func (c *Client) getJSON(ctx context.Context, endpoint string, target any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("User-Agent", config.DesktopUserAgent)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Referer", c.siteBase+"/")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("do request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return &provider.HTTPError{Code: resp.StatusCode, URL: endpoint}
	}

	body, err := httpclient.ReadCapped(resp)
	if err != nil {
		return fmt.Errorf("read body: %w", err)
	}

	if err := json.Unmarshal(body, target); err != nil {
		return fmt.Errorf("decode json: %w", err)
	}
	return nil
}

func parseHandle(handle string) (kind, id string) {
	trimmed := strings.TrimPrefix(handle, handlePrefix)
	if strings.HasPrefix(trimmed, "event:") {
		return "event", strings.TrimPrefix(trimmed, "event:")
	}
	if strings.HasPrefix(trimmed, "channel:") {
		return "channel", strings.TrimPrefix(trimmed, "channel:")
	}
	if strings.HasPrefix(trimmed, "event_") {
		return "event", trimmed
	}
	return "channel", trimmed
}

func matchesEvent(ev stabilityEvent, needle string) bool {
	if needle == "" {
		return true
	}
	return strings.Contains(strings.ToLower(ev.Title), needle) ||
		strings.Contains(strings.ToLower(ev.Category), needle) ||
		strings.Contains(strings.ToLower(ev.Team1), needle) ||
		strings.Contains(strings.ToLower(ev.Team2), needle)
}

func toEventResult(ev stabilityEvent, now time.Time) provider.SearchResult {
	startsAt := parseEventStart(ev)
	isLive := false
	if !startsAt.IsZero() {
		isLive = !startsAt.After(now) && now.Sub(startsAt) <= provider.LiveEventMaxDuration
	}
	group := strings.TrimSpace(ev.Category)
	if group == "" {
		group = "Sports"
	}
	var genres []string
	if group != "" {
		genres = []string{group}
	}

	return provider.SearchResult{
		ID:        handlePrefix + "event:" + ev.ID,
		Title:     ev.Title,
		Type:      provider.ModeLive,
		MediaType: provider.MediaTypeLive,
		CoverURL:  ev.Poster,
		Genres:    genres,
		Group:     group,
		StartsAt:  startsAt,
		Live:      isLive,
	}
}

func toChannelResult(ch channelSearchItem) provider.SearchResult {
	return provider.SearchResult{
		ID:        handlePrefix + "channel:" + ch.ID,
		Title:     ch.Name,
		Type:      provider.ModeLive,
		MediaType: provider.MediaTypeLive,
		CoverURL:  ch.Icon,
		Group:     "Channel",
		Live:      true,
	}
}

func parseEventStart(ev stabilityEvent) time.Time {
	if ev.Timestamp > 1e11 {
		return time.UnixMilli(ev.Timestamp).Local()
	} else if ev.Timestamp > 0 {
		return time.Unix(ev.Timestamp, 0).Local()
	}
	if ev.Date != "" && ev.Time != "" {
		combined := ev.Date + " " + ev.Time
		for _, layout := range []string{"2006-01-02 15:04", "2006-01-02 3:04 PM", "2006-01-02 03:04 PM"} {
			if t, err := time.ParseInLocation(layout, combined, time.Local); err == nil {
				return t
			}
		}
		if ts, err := strconv.ParseInt(ev.Time, 10, 64); err == nil && ts > 0 {
			if ts > 1e11 {
				return time.UnixMilli(ts).Local()
			}
			return time.Unix(ts, 0).Local()
		}
	}
	return time.Time{}
}
