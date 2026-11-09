// Package iptvorg implements provider.Provider for the IPTV-org live TV
// directory (https://github.com/iptv-org). IPTV-org publishes its catalog as
// public JSON documents on GitHub Pages: channel metadata, direct stream URLs,
// category names, and country metadata are separate files joined by channel
// ID. Kari reads those catalogs, exposes every channel with at least one
// directly playable stream, and resolves all of the channel's mirrors.
package iptvorg

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"
	"golang.org/x/sync/singleflight"

	"kari/internal/config"
	"kari/internal/httpclient"
	"kari/internal/lang"
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

// iptvLog scopes every line from this package with its identity.
var iptvLog = logging.With("provider", "iptvorg")

const (
	// handlePrefix marks IPTV-org channel handles in search results and
	// history. Resolution also accepts a bare channel ID from older entries.
	handlePrefix = "iptvorg:"
	// catalogTTL bounds how long the in-memory catalog is reused. The
	// upstream repository updates several times a day.
	catalogTTL = 6 * time.Hour
	// catalogFetchTimeout is independent of the caller's search budget: the
	// fetch is shared by every concurrent search, so one caller timing out
	// must not cancel the refresh for the others.
	catalogFetchTimeout = 30 * time.Second
	// maxCatalogBytes caps each JSON document. The channel catalog is the
	// largest at roughly 8 MB; the limit only guards against runaway bodies.
	maxCatalogBytes = 32 << 20
	// catalogCacheKey is the singleflight key for the catalog refresh.
	catalogCacheKey = "catalog"
)

// iptvChannel is one IPTV-org channel entry. The JSON field names are the
// upstream catalog vocabulary, kept private to this package. countryName,
// categoryNames, languages, and streams are derived at load time.
type iptvChannel struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	AltNames    []string `json:"alt_names"`
	Network     string   `json:"network"`
	Country     string   `json:"country"`
	Categories  []string `json:"categories"`
	IsNSFW      bool     `json:"is_nsfw"`
	Closed      string   `json:"closed"`
	countryName string
	categories  []string
	languages   []string
	streams     []iptvStream
}

// iptvStream is one direct playback URL belonging to a channel.
type iptvStream struct {
	Channel   string `json:"channel"`
	Title     string `json:"title"`
	URL       string `json:"url"`
	Quality   string `json:"quality"`
	UserAgent string `json:"user_agent"`
	Referrer  string `json:"referrer"`
}

// iptvCategory maps a category ID to its display name.
type iptvCategory struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// iptvCountry maps an ISO country code to its display name and languages.
type iptvCountry struct {
	Name      string   `json:"name"`
	Code      string   `json:"code"`
	Languages []string `json:"languages"`
}

// endpoints groups the four catalog document URLs so construction stays
// readable.
type endpoints struct {
	channels   string
	streams    string
	categories string
	countries  string
}

// Client implements provider.Provider against the IPTV-org live catalog.
type Client struct {
	httpClient *http.Client
	endpoints  endpoints
	sf         singleflight.Group
	mu         sync.Mutex
	channels   []iptvChannel
	fetchedAt  time.Time
}

// NewClient constructs the IPTV-org provider against the public catalogs.
func NewClient() (*Client, error) {
	return newClient(endpoints{
		channels:   config.IPTVOrgChannelsURL,
		streams:    config.IPTVOrgStreamsURL,
		categories: config.IPTVOrgCategoriesURL,
		countries:  config.IPTVOrgCountriesURL,
	}, httpclient.NewWithUserAgentAndTimeout(config.DesktopUserAgent, 30*time.Second))
}

// newClient constructs the provider with explicit catalog endpoints and an
// HTTP client, used by tests and mock environments.
func newClient(e endpoints, hc *http.Client) (*Client, error) {
	for name, raw := range map[string]string{
		"channels":   e.channels,
		"streams":    e.streams,
		"categories": e.categories,
		"countries":  e.countries,
	} {
		parsed, err := url.Parse(strings.TrimSpace(raw))
		if err != nil {
			return nil, fmt.Errorf("iptvorg: invalid %s URL %q: %w", name, raw, err)
		}
		if parsed.Scheme == "" || parsed.Host == "" {
			return nil, fmt.Errorf("iptvorg: invalid %s URL %q", name, raw)
		}
	}
	if hc == nil {
		hc = httpclient.NewWithUserAgentAndTimeout(config.DesktopUserAgent, 30*time.Second)
	}
	return &Client{httpClient: hc, endpoints: e}, nil
}

// Name implements provider.Provider.
func (c *Client) Name() string { return "iptvorg" }

// Alias implements provider.Presenter.
func (c *Client) Alias() string { return "IPTV-org" }

// Modes implements provider.Provider. IPTV-org serves live TV channels only.
func (c *Client) Modes() []provider.Mode {
	return []provider.Mode{{Name: provider.ModeLive, Priority: 4}}
}

// Features implements provider.FeatureSource.
func (c *Client) Features(mode provider.ContentType) provider.Features {
	if mode != provider.ModeLive {
		return provider.Features{}
	}
	return provider.Features{
		AllowEmptyQuery:   true,
		NoCachedSearches:  true,
		SearchPlaceholder: "Search live TV channels, countries, or categories…",
	}
}

// Search lists channels matching query by name, country, category, language,
// or network. An empty query returns the whole playable catalog, ordered by
// country and channel name so the live list is browsable without typing.
func (c *Client) Search(ctx context.Context, query string, mode provider.ContentType) ([]provider.SearchResult, error) {
	if mode != provider.ModeLive {
		return nil, provider.ErrNoResults
	}
	channels, err := c.catalog(ctx)
	if err != nil {
		return nil, fmt.Errorf("iptvorg search: %w", err)
	}

	needle := strings.ToLower(strings.TrimSpace(query))
	results := make([]provider.SearchResult, 0, len(channels))
	for _, ch := range channels {
		if needle != "" && !matches(ch, needle) {
			continue
		}
		results = append(results, searchResult(ch))
	}
	if len(results) == 0 {
		return nil, provider.ErrNoResults
	}
	return results, nil
}

// FetchEpisodes returns the single playable unit a live channel has. The live
// flow resolves straight from the search result, but history resumes and the
// generic pipeline still expect an episode listing.
func (c *Client) FetchEpisodes(_ context.Context, series provider.SearchResult) ([]provider.Episode, error) {
	if strings.TrimSpace(series.ID) == "" {
		return nil, provider.ErrNoEpisodes
	}
	return []provider.Episode{{Title: series.Title, ID: series.ID}}, nil
}

// ResolveSource resolves every direct stream the channel advertises.
func (c *Client) ResolveSource(ctx context.Context, mediaID string, episode provider.Episode) ([]provider.MediaSource, error) {
	handle := mediaID
	if _, ok := parseHandle(handle); !ok {
		handle = episode.ID
	}
	channelID, ok := parseHandle(handle)
	if !ok {
		return nil, fmt.Errorf("iptvorg resolve: invalid channel handle %q: %w", handle, provider.ErrNoSources)
	}

	channels, err := c.catalog(ctx)
	if err != nil {
		return nil, fmt.Errorf("iptvorg resolve: %w", err)
	}
	for _, ch := range channels {
		if ch.ID != channelID {
			continue
		}
		sources := make([]provider.MediaSource, 0, len(ch.streams))
		audioLabel := lang.Names(ch.languages)
		for _, stream := range ch.streams {
			sources = append(sources, provider.MediaSource{
				URL:        stream.URL,
				Quality:    qualityLabel(stream.Quality),
				Type:       streamType(stream.URL),
				Referer:    strings.TrimSpace(stream.Referrer),
				UserAgent:  strings.TrimSpace(stream.UserAgent),
				AudioLabel: audioLabel,
			})
		}
		if len(sources) > 0 {
			return sources, nil
		}
		break
	}
	return nil, fmt.Errorf("iptvorg channel %q not in catalog: %w", channelID, provider.ErrNoSources)
}

// VerifyLiveStream implements provider.LiveStreamVerifier.
func (c *Client) VerifyLiveStream(ctx context.Context, _ provider.ContentType, source provider.MediaSource) error {
	return provider.VerifyLiveStream(ctx, c.httpClient, source)
}

// catalog returns the cached playable catalog, refreshing it when stale. A
// failed refresh keeps serving the previous catalog so a transient GitHub
// Pages outage does not empty the live tab.
func (c *Client) catalog(ctx context.Context) ([]iptvChannel, error) {
	if cached, ok := c.freshChannels(); ok {
		return cached, nil
	}

	result := c.sf.DoChan(catalogCacheKey, func() (any, error) {
		if cached, ok := c.freshChannels(); ok {
			return cached, nil
		}
		fetchCtx, cancel := context.WithTimeout(context.Background(), catalogFetchTimeout)
		defer cancel()
		channels, err := c.fetchCatalog(fetchCtx)
		if err != nil {
			if stale := c.cachedChannels(); len(stale) > 0 {
				iptvLog.Debug("catalog refresh failed; serving cached channels", "err", err)
				return stale, nil
			}
			return nil, err
		}
		c.mu.Lock()
		c.channels = channels
		c.fetchedAt = time.Now()
		c.mu.Unlock()
		return channels, nil
	})

	select {
	case res := <-result:
		if res.Err != nil {
			return nil, res.Err
		}
		channels, ok := res.Val.([]iptvChannel)
		if !ok || len(channels) == 0 {
			return nil, fmt.Errorf("catalog is empty: %w", provider.ErrUpstreamChanged)
		}
		return channels, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// freshChannels returns the cached catalog while it is within its TTL.
func (c *Client) freshChannels() ([]iptvChannel, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.channels) == 0 || time.Since(c.fetchedAt) >= catalogTTL {
		return nil, false
	}
	return c.channels, true
}

// cachedChannels returns the cached catalog regardless of age.
func (c *Client) cachedChannels() []iptvChannel {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.channels
}

// fetchCatalog downloads all four documents in parallel and joins them.
func (c *Client) fetchCatalog(ctx context.Context) ([]iptvChannel, error) {
	var (
		channels   []iptvChannel
		streams    []iptvStream
		categories []iptvCategory
		countries  []iptvCountry
	)
	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error { return c.getJSON(gctx, c.endpoints.channels, &channels) })
	g.Go(func() error { return c.getJSON(gctx, c.endpoints.streams, &streams) })
	g.Go(func() error { return c.getJSON(gctx, c.endpoints.categories, &categories) })
	g.Go(func() error { return c.getJSON(gctx, c.endpoints.countries, &countries) })
	if err := g.Wait(); err != nil {
		return nil, err
	}
	return buildCatalog(channels, streams, categories, countries)
}

// getJSON fetches one catalog document.
func (c *Client) getJSON(ctx context.Context, endpoint string, target any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("fetch %s: %w", endpoint, err)
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return &provider.HTTPError{Code: resp.StatusCode, URL: endpoint}
	}
	body, err := readCatalogBody(resp)
	if err != nil {
		return fmt.Errorf("read %s: %w", endpoint, err)
	}
	if err := json.Unmarshal(body, target); err != nil {
		return fmt.Errorf("decode %s: %w", endpoint, err)
	}
	return nil
}

// readCatalogBody reads a catalog document with a cap large enough for the
// full channel list, which exceeds httpclient's generic JSON limit.
func readCatalogBody(resp *http.Response) ([]byte, error) {
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxCatalogBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}
	if len(body) > maxCatalogBytes {
		return nil, fmt.Errorf("catalog exceeds %d bytes: %w", maxCatalogBytes, provider.ErrUpstreamChanged)
	}
	return body, nil
}

// buildCatalog joins channel metadata with direct streams and display names,
// keeping only entries Kari can actually play. Closed and adult channels are
// omitted so the browsable catalog stays useful and safe.
func buildCatalog(channels []iptvChannel, streams []iptvStream, categories []iptvCategory, countries []iptvCountry) ([]iptvChannel, error) {
	if len(channels) == 0 || len(streams) == 0 {
		return nil, fmt.Errorf("catalog documents are empty: %w", provider.ErrUpstreamChanged)
	}

	categoryNames := make(map[string]string, len(categories))
	for _, category := range categories {
		if id := strings.TrimSpace(category.ID); id != "" {
			categoryNames[id] = strings.TrimSpace(category.Name)
		}
	}
	countryByCode := make(map[string]iptvCountry, len(countries))
	for _, country := range countries {
		if code := strings.ToUpper(strings.TrimSpace(country.Code)); code != "" {
			countryByCode[code] = country
		}
	}

	streamsByChannel := make(map[string][]iptvStream)
	seenURLs := make(map[string]map[string]struct{})
	for _, stream := range streams {
		channelID := strings.TrimSpace(stream.Channel)
		streamURL := strings.TrimSpace(stream.URL)
		if channelID == "" || streamURL == "" || !provider.IsDirectURL(streamURL) {
			continue
		}
		if seenURLs[channelID] == nil {
			seenURLs[channelID] = make(map[string]struct{})
		}
		if _, ok := seenURLs[channelID][streamURL]; ok {
			continue
		}
		seenURLs[channelID][streamURL] = struct{}{}
		stream.URL = streamURL
		streamsByChannel[channelID] = append(streamsByChannel[channelID], stream)
	}

	out := make([]iptvChannel, 0, len(channels))
	for _, channel := range channels {
		id := strings.TrimSpace(channel.ID)
		name := strings.TrimSpace(channel.Name)
		if id == "" || name == "" || strings.TrimSpace(channel.Closed) != "" || channel.IsNSFW {
			continue
		}
		channelStreams := streamsByChannel[id]
		if len(channelStreams) == 0 {
			continue
		}
		channel.streams = channelStreams
		categoryIDs := compactStrings(channel.Categories)
		channel.categories = make([]string, 0, len(categoryIDs))
		for _, categoryID := range categoryIDs {
			if display := categoryNames[categoryID]; display != "" {
				channel.categories = append(channel.categories, display)
			}
		}
		if country, ok := countryByCode[strings.ToUpper(strings.TrimSpace(channel.Country))]; ok {
			channel.countryName = strings.TrimSpace(country.Name)
			channel.languages = compactStrings(country.Languages)
		}
		if channel.countryName == "" {
			channel.countryName = strings.ToUpper(strings.TrimSpace(channel.Country))
			if channel.countryName == "" {
				channel.countryName = "Live TV"
			}
		}
		out = append(out, channel)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("catalog has no playable channels: %w", provider.ErrUpstreamChanged)
	}
	sortChannels(out)
	return out, nil
}

// compactStrings trims values and drops empty or duplicate entries while
// preserving order.
func compactStrings(values []string) []string {
	out := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		key := strings.ToLower(value)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, value)
	}
	return out
}

// matches reports whether query (already lowercased) names the channel, its
// country, category, language, or network. Two-letter queries only match exact
// country/language codes so "in" selects India rather than every country name
// containing those letters.
func matches(ch iptvChannel, query string) bool {
	if strings.Contains(strings.ToLower(ch.Name), query) {
		return true
	}
	for _, alt := range ch.AltNames {
		if strings.Contains(strings.ToLower(alt), query) {
			return true
		}
	}
	if strings.Contains(strings.ToLower(ch.Network), query) {
		return true
	}
	if strings.EqualFold(strings.TrimSpace(ch.Country), query) {
		return true
	}
	if len(query) >= 3 && strings.Contains(strings.ToLower(ch.countryName), query) {
		return true
	}
	for _, category := range ch.categories {
		if len(query) >= 3 && strings.Contains(strings.ToLower(category), query) {
			return true
		}
	}
	for _, code := range ch.languages {
		if strings.EqualFold(code, query) {
			return true
		}
		if len(query) >= 3 && strings.Contains(strings.ToLower(lang.Name(code)), query) {
			return true
		}
	}
	return false
}

// searchResult maps one catalog entry to a live search result. Channels are
// always-on, so Live is set and no schedule is implied. The first genre is the
// category so the live screen groups channels by genre; country and language
// follow so search can match them through the generic genre filter.
func searchResult(ch iptvChannel) provider.SearchResult {
	genres := make([]string, 0, len(ch.categories)+len(ch.languages)+1)
	genres = append(genres, ch.categories...)
	genres = append(genres, ch.countryName)
	for _, code := range ch.languages {
		if name := lang.Name(code); name != "" {
			genres = append(genres, name)
		}
	}
	return provider.SearchResult{
		Title:     ch.Name,
		ID:        channelHandle(ch.ID),
		Type:      provider.ModeLive,
		MediaType: provider.MediaTypeLive,
		Live:      true,
		Genres:    genres,
		Group:     "Channel",
	}
}

// sortChannels orders the catalog by country display name, then channel name,
// so an empty live search reads as a country-grouped channel list.
func sortChannels(channels []iptvChannel) {
	sort.SliceStable(channels, func(i, j int) bool {
		left, right := channels[i].countryName, channels[j].countryName
		if left != right {
			return left < right
		}
		return strings.ToLower(channels[i].Name) < strings.ToLower(channels[j].Name)
	})
}

// qualityLabel keeps the upstream quality label, falling back to Auto.
func qualityLabel(raw string) string {
	if label := strings.TrimSpace(raw); label != "" {
		return label
	}
	return "Auto"
}

// streamType classifies a channel stream for the player layer. IPTV-org is
// dominated by HLS manifests; a direct mp4 URL is the only other shape seen.
func streamType(streamURL string) string {
	if strings.Contains(strings.ToLower(streamURL), ".mp4") {
		return provider.SourceTypeMP4
	}
	return provider.SourceTypeHLS
}

// channelHandle builds the provider-specific handle stored in search results
// and history.
func channelHandle(id string) string {
	return handlePrefix + id
}

// parseHandle extracts a channel ID from a prefixed or bare handle.
func parseHandle(handle string) (string, bool) {
	id := strings.TrimSpace(handle)
	id = strings.TrimPrefix(id, handlePrefix)
	if id == "" {
		return "", false
	}
	return id, true
}
