// Package famelack implements provider.Provider for the Famelack live TV
// directory (https://famelack.com, formerly TV.Garden). Famelack publishes
// its channel catalog as gzip-compressed JSON in a public GitHub data
// repository; each channel carries direct HTTP(S) stream URLs. Kari reads
// that catalog, exposes it through the live mode, and resolves the channel's
// streams without a proxy.
package famelack

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"

	"kari/internal/config"
	"kari/internal/httpclient"
	"kari/internal/lang"
	"kari/internal/logging"
	"kari/internal/provider"
)

var (
	_ provider.Provider      = (*Client)(nil)
	_ provider.EpisodeSource = (*Client)(nil)
	_ provider.FeatureSource = (*Client)(nil)
	_ provider.Presenter     = (*Client)(nil)
)

// famelackLog scopes every line from this package with its identity.
var famelackLog = logging.With("provider", "famelack")

const (
	// handlePrefix marks Famelack channel handles in search results and
	// history. Resolution also accepts a bare nanoid from older entries.
	handlePrefix = "famelack:"
	// directoryTTL bounds how long the in-memory catalog is reused before
	// a search refreshes it. The upstream repository updates a few times
	// a day, so a short TTL would only add requests.
	directoryTTL = 6 * time.Hour
	// directoryFetchTimeout is independent of the caller's search budget:
	// the fetch is shared by every concurrent search, so one caller
	// timing out must not cancel the refresh for the others.
	directoryFetchTimeout = 20 * time.Second
	// countriesFetchTimeout bounds the cosmetic country-name lookup so a
	// slow metadata host cannot hold up an otherwise loaded catalog.
	countriesFetchTimeout = 3 * time.Second
	// maxDirectoryBytes caps the decompressed catalog. The current file
	// is ~1.5 MB; the limit only guards against malformed gzip.
	maxDirectoryBytes = 16 << 20
	// directoryCacheKey is the singleflight key for the catalog refresh.
	directoryCacheKey = "channels"
)

// channel is one Famelack directory entry. The JSON field names are
// Famelack's compressed catalog vocabulary, kept private to this package.
// countryName is derived at load time from the country metadata catalog.
type channel struct {
	Nanoid      string         `json:"nanoid"`
	Name        string         `json:"name"`
	Sources     channelSources `json:"sources"`
	Languages   []string       `json:"languages"`
	Country     string         `json:"country"`
	countryName string
}

// channelSources groups the playback URLs Famelack lists for a channel.
// Only direct streams are read; YouTube embeds are ignored because Kari
// plays direct streams only.
type channelSources struct {
	Streams []string `json:"streams"`
}

// countryMetadata is one entry of Famelack's country display-name catalog.
type countryMetadata struct {
	Country string `json:"country"`
}

// Client implements provider.Provider against the Famelack live TV catalog.
type Client struct {
	httpClient   *http.Client
	channelsURL  string
	countriesURL string
	sf           singleflight.Group
	mu           sync.Mutex
	channels     []channel
	fetchedAt    time.Time
}

// NewClient constructs the Famelack provider against the public catalog.
func NewClient() (*Client, error) {
	return NewClientWithURLs(
		config.FamelackTVChannelsURL,
		config.FamelackTVCountriesURL,
		httpclient.NewWithUserAgentAndTimeout(config.DesktopUserAgent, 20*time.Second),
	)
}

// NewClientWithURLs constructs the Famelack provider with custom catalog
// URLs and HTTP client for testing and mock environments.
func NewClientWithURLs(channelsURL, countriesURL string, hc *http.Client) (*Client, error) {
	channelsURL = strings.TrimSpace(channelsURL)
	countriesURL = strings.TrimSpace(countriesURL)
	if channelsURL == "" {
		return nil, fmt.Errorf("famelack: empty channels URL")
	}
	if countriesURL == "" {
		return nil, fmt.Errorf("famelack: empty countries URL")
	}
	if hc == nil {
		hc = httpclient.NewWithUserAgentAndTimeout(config.DesktopUserAgent, 20*time.Second)
	}
	return &Client{httpClient: hc, channelsURL: channelsURL, countriesURL: countriesURL}, nil
}

// Name implements provider.Provider.
func (c *Client) Name() string { return "famelack" }

// Alias implements provider.Presenter.
func (c *Client) Alias() string { return "Famelack" }

// Modes implements provider.Provider. Famelack only serves live TV channels;
// its priority trails Pengu's live sports catalog so event rows stay first.
func (c *Client) Modes() []provider.Mode {
	return []provider.Mode{{Name: provider.ModeLive, Priority: 2}}
}

// Features implements provider.FeatureSource.
func (c *Client) Features(mode provider.ContentType) provider.Features {
	if mode != provider.ModeLive {
		return provider.Features{}
	}
	return provider.Features{
		AllowEmptyQuery:   true,
		NoCachedSearches:  true,
		SearchPlaceholder: "Search live TV channels or a country…",
	}
}

// Search lists Famelack channels matching query by name, country, or
// language. An empty query returns the whole playable catalog, ordered by
// country and channel name so the live list is browsable without typing.
func (c *Client) Search(ctx context.Context, query string, mode provider.ContentType) ([]provider.SearchResult, error) {
	if mode != provider.ModeLive {
		return nil, provider.ErrNoResults
	}
	channels, err := c.directory(ctx)
	if err != nil {
		return nil, fmt.Errorf("famelack search: %w", err)
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

// FetchEpisodes returns the single playable unit a live channel has. The
// live flow resolves straight from the search result, but history resumes
// and the generic pipeline still expects an episode listing.
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
	nanoid, ok := parseHandle(handle)
	if !ok {
		return nil, fmt.Errorf("famelack resolve: invalid channel handle %q: %w", handle, provider.ErrNoSources)
	}

	channels, err := c.directory(ctx)
	if err != nil {
		return nil, fmt.Errorf("famelack resolve: %w", err)
	}
	for _, ch := range channels {
		if ch.Nanoid != nanoid {
			continue
		}
		streams := playableStreams(ch)
		if len(streams) == 0 {
			break
		}
		sources := make([]provider.MediaSource, 0, len(streams))
		for _, streamURL := range streams {
			sources = append(sources, provider.MediaSource{
				URL:     streamURL,
				Quality: "Auto",
				Type:    streamType(streamURL),
			})
		}
		return sources, nil
	}
	return nil, fmt.Errorf("famelack channel %q not in directory: %w", nanoid, provider.ErrNoSources)
}

// directory returns the cached playable catalog, refreshing it when stale.
// A failed refresh keeps serving the previous catalog so a transient GitHub
// outage does not empty the live tab.
func (c *Client) directory(ctx context.Context) ([]channel, error) {
	if cached, ok := c.freshChannels(); ok {
		return cached, nil
	}

	result := c.sf.DoChan(directoryCacheKey, func() (any, error) {
		if cached, ok := c.freshChannels(); ok {
			return cached, nil
		}
		fetchCtx, cancel := context.WithTimeout(context.Background(), directoryFetchTimeout)
		defer cancel()
		channels, err := c.fetchDirectory(fetchCtx)
		if err != nil {
			if stale := c.cachedChannels(); len(stale) > 0 {
				famelackLog.Debug("catalog refresh failed; serving cached channels", "err", err)
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
		channels, ok := res.Val.([]channel)
		if !ok || len(channels) == 0 {
			return nil, fmt.Errorf("catalog is empty: %w", provider.ErrUpstreamChanged)
		}
		return channels, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// freshChannels returns the cached catalog while it is within its TTL.
func (c *Client) freshChannels() ([]channel, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.channels) == 0 || time.Since(c.fetchedAt) >= directoryTTL {
		return nil, false
	}
	return c.channels, true
}

// cachedChannels returns the cached catalog regardless of age.
func (c *Client) cachedChannels() []channel {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.channels
}

// fetchDirectory downloads and decodes the gzip-compressed catalog, keeping
// only entries Kari can actually play.
func (c *Client) fetchDirectory(ctx context.Context) ([]channel, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.channelsURL, nil)
	if err != nil {
		return nil, fmt.Errorf("create catalog request: %w", err)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch catalog: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, &provider.HTTPError{Code: resp.StatusCode, URL: c.channelsURL}
	}

	body, err := httpclient.ReadCapped(resp)
	if err != nil {
		return nil, fmt.Errorf("read catalog body: %w", err)
	}
	gz, err := gzip.NewReader(bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("open catalog gzip: %w", err)
	}
	defer gz.Close()

	raw, err := io.ReadAll(io.LimitReader(gz, maxDirectoryBytes+1))
	if err != nil {
		return nil, fmt.Errorf("decompress catalog: %w", err)
	}
	if len(raw) > maxDirectoryBytes {
		return nil, fmt.Errorf("catalog exceeds %d bytes: %w", maxDirectoryBytes, provider.ErrUpstreamChanged)
	}

	var entries []channel
	if err := json.Unmarshal(raw, &entries); err != nil {
		return nil, fmt.Errorf("decode catalog: %w", err)
	}

	countriesCtx, cancelCountries := context.WithTimeout(ctx, countriesFetchTimeout)
	names := c.fetchCountryNames(countriesCtx)
	cancelCountries()
	channels := make([]channel, 0, len(entries))
	for _, entry := range entries {
		if strings.TrimSpace(entry.Nanoid) == "" || strings.TrimSpace(entry.Name) == "" {
			continue
		}
		if len(playableStreams(entry)) == 0 {
			continue
		}
		entry.countryName = countryName(entry.Country, names)
		channels = append(channels, entry)
	}
	if len(channels) == 0 {
		return nil, fmt.Errorf("catalog has no playable channels: %w", provider.ErrUpstreamChanged)
	}
	sortChannels(channels)
	return channels, nil
}

// fetchCountryNames downloads the country display-name catalog. It is
// cosmetic, so a failure only logs and falls back to uppercase country codes.
func (c *Client) fetchCountryNames(ctx context.Context) map[string]string {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.countriesURL, nil)
	if err != nil {
		famelackLog.Debug("country metadata request construction failed", "err", err)
		return nil
	}
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		famelackLog.Debug("country metadata request failed", "err", err)
		return nil
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		famelackLog.Debug("country metadata returned non-200", "status", resp.StatusCode)
		return nil
	}

	body, err := httpclient.ReadCapped(resp)
	if err != nil {
		famelackLog.Debug("country metadata read failed", "err", err)
		return nil
	}
	gz, err := gzip.NewReader(bytes.NewReader(body))
	if err != nil {
		famelackLog.Debug("country metadata gzip failed", "err", err)
		return nil
	}
	defer gz.Close()

	raw, err := io.ReadAll(io.LimitReader(gz, maxDirectoryBytes+1))
	if err != nil {
		famelackLog.Debug("country metadata decompress failed", "err", err)
		return nil
	}

	var metadata map[string]countryMetadata
	if err := json.Unmarshal(raw, &metadata); err != nil {
		famelackLog.Debug("country metadata decode failed", "err", err)
		return nil
	}
	names := make(map[string]string, len(metadata))
	for code, entry := range metadata {
		if name := strings.TrimSpace(entry.Country); name != "" {
			names[strings.ToUpper(code)] = name
		}
	}
	return names
}

// matches reports whether query (already lowercased) names the channel, its
// country, or one of its languages. Two-letter queries only match exact
// country/language codes: substring matching would otherwise make "in" hit
// "United Kingdom" and "Hindi" instead of selecting Indian channels.
func matches(ch channel, query string) bool {
	if strings.Contains(strings.ToLower(ch.Name), query) {
		return true
	}
	if strings.EqualFold(strings.TrimSpace(ch.Country), query) {
		return true
	}
	if len(query) >= 3 && strings.Contains(strings.ToLower(ch.countryName), query) {
		return true
	}
	for _, code := range ch.Languages {
		if strings.EqualFold(strings.TrimSpace(code), query) {
			return true
		}
		if len(query) >= 3 && strings.Contains(strings.ToLower(lang.Name(code)), query) {
			return true
		}
	}
	return false
}

// searchResult maps one catalog entry to a live search result. Famelack
// channels are always-on, so Live is set and no schedule is implied.
func searchResult(ch channel) provider.SearchResult {
	genres := make([]string, 0, len(ch.Languages)+1)
	if ch.countryName != "" {
		genres = append(genres, ch.countryName)
	}
	for _, code := range ch.Languages {
		if name := lang.Name(code); name != "" {
			genres = append(genres, name)
		}
	}
	return provider.SearchResult{
		Title:     ch.Name,
		ID:        channelHandle(ch.Nanoid),
		Type:      provider.ModeLive,
		MediaType: provider.MediaTypeLive,
		Live:      true,
		Genres:    genres,
		Group:     "Channel",
	}
}

// sortChannels orders the catalog by country display name, then channel name,
// so an empty live search reads as a country-grouped channel list.
func sortChannels(channels []channel) {
	sort.SliceStable(channels, func(i, j int) bool {
		left, right := channels[i].countryName, channels[j].countryName
		if left != right {
			return left < right
		}
		return strings.ToLower(channels[i].Name) < strings.ToLower(channels[j].Name)
	})
}

// countryName resolves a country code through the metadata catalog, falling
// back to the uppercase code when the entry is missing.
func countryName(code string, names map[string]string) string {
	code = strings.TrimSpace(code)
	if code == "" {
		return "Live TV"
	}
	upper := strings.ToUpper(code)
	if name := names[upper]; name != "" {
		return name
	}
	return upper
}

// playableStreams returns the channel's deduplicated direct HTTP(S) streams,
// dropping YouTube embeds and anything mpv cannot open itself.
func playableStreams(ch channel) []string {
	streams := make([]string, 0, len(ch.Sources.Streams))
	seen := make(map[string]struct{}, len(ch.Sources.Streams))
	for _, raw := range ch.Sources.Streams {
		streamURL := strings.TrimSpace(raw)
		if streamURL == "" || !provider.IsDirectURL(streamURL) {
			continue
		}
		if _, ok := seen[streamURL]; ok {
			continue
		}
		seen[streamURL] = struct{}{}
		streams = append(streams, streamURL)
	}
	return streams
}

// streamType classifies a channel stream for the player layer. Famelack
// serves HLS manifests; a direct mp4 URL is the only other shape present.
func streamType(streamURL string) string {
	if strings.Contains(strings.ToLower(streamURL), ".mp4") {
		return provider.SourceTypeMP4
	}
	return provider.SourceTypeHLS
}

// channelHandle builds the provider-specific handle stored in search results
// and history.
func channelHandle(nanoid string) string {
	return handlePrefix + nanoid
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
