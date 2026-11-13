// Package timstreams implements provider.Provider for TimStreams live sports
// events and 24/7 channels. Its API supplies event metadata and short stream
// links; playback tickets are resolved directly by the provider without a
// hosted proxy.
package timstreams

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
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
	_ provider.DeadlineSource     = (*Client)(nil)
	_ provider.Presenter          = (*Client)(nil)
	_ provider.LiveStreamVerifier = (*Client)(nil)
)

var timStreamsLog = logging.With("provider", "timstreams")

const handlePrefix = "timstreams:"

// event is one scheduled sporting event or always-on channel from the API.
type event struct {
	URL      string   `json:"url"`
	Name     string   `json:"name"`
	Logo     string   `json:"logo"`
	Genre    int      `json:"genre"`
	SubGenre int      `json:"sub_genre"`
	Time     string   `json:"time"`
	VIP      bool     `json:"vip"`
	Flag     string   `json:"flag"`
	Streams  []stream `json:"streams"`
}

type stream struct {
	Name string `json:"name"`
	URL  string `json:"url"`
	VIP  bool   `json:"vip"`
}

type genre struct {
	ID            int           `json:"id"`
	Name          string        `json:"name"`
	SubCategories []subCategory `json:"sub_categories"`
}

type subCategory struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

type eventCatalog struct {
	Events []event `json:"events"`
	Genres []genre `json:"genres"`
}

type channelCatalog struct {
	Channels []event `json:"channels"`
	Genres   []genre `json:"genres"`
}

type watchResponse struct {
	Item event `json:"item"`
}

// Client implements provider.Provider for TimStreams.
type Client struct {
	httpClient *http.Client
	apiBase    string
	shortBase  string
}

// NewClient constructs the provider with Kari's shared HTTP client.
func NewClient() (*Client, error) {
	return NewClientWithURLs(
		config.TimStreamsAPIBase,
		config.TimStreamsShortBase,
		httpclient.NewWithUserAgentAndTimeout(config.DesktopUserAgent, 25*time.Second),
	)
}

// NewClientWithURLs constructs a client with custom service URLs and HTTP client.
func NewClientWithURLs(apiBase, shortBase string, hc *http.Client) (*Client, error) {
	apiBase = strings.TrimRight(strings.TrimSpace(apiBase), "/")
	shortBase = strings.TrimRight(strings.TrimSpace(shortBase), "/")
	if !validBaseURL(apiBase) {
		return nil, fmt.Errorf("timstreams: invalid API base URL %q", apiBase)
	}
	if !validBaseURL(shortBase) {
		return nil, fmt.Errorf("timstreams: invalid short-link base URL %q", shortBase)
	}
	if hc == nil {
		hc = httpclient.NewWithUserAgentAndTimeout(config.DesktopUserAgent, 25*time.Second)
	}
	return &Client{httpClient: hc, apiBase: apiBase, shortBase: shortBase}, nil
}

func validBaseURL(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && (u.Scheme == "https" || u.Scheme == "http") && u.Hostname() != ""
}

// Name implements provider.Provider.
func (c *Client) Name() string { return "timstreams" }

// Alias implements provider.Presenter.
func (c *Client) Alias() string { return "TimStreams" }

// Modes implements provider.Provider.
func (c *Client) Modes() []provider.Mode {
	return []provider.Mode{{Name: provider.ModeLive, Priority: 3}}
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

// Deadlines gives the multi-request ticket resolver enough time to try lanes.
func (c *Client) Deadlines(mode provider.ContentType) provider.Deadlines {
	if mode == provider.ModeLive {
		return provider.Deadlines{Source: 40 * time.Second}
	}
	return provider.Deadlines{}
}

// Search returns matching live events and 24/7 channels. The API's live-upcoming
// catalog is queried alongside channels so a slow channel feed cannot hold up
// sporting events.
func (c *Client) Search(ctx context.Context, query string, mode provider.ContentType) ([]provider.SearchResult, error) {
	if mode != provider.ModeLive {
		return nil, provider.ErrNoResults
	}
	type result struct {
		events   eventCatalog
		channels channelCatalog
		err      error
		kind     string
	}
	results := make(chan result, 2)
	go func() {
		var catalog eventCatalog
		err := c.getJSON(ctx, c.apiBase+"/live-upcoming", &catalog)
		results <- result{events: catalog, err: err, kind: "events"}
	}()
	go func() {
		var catalog channelCatalog
		err := c.getJSON(ctx, c.apiBase+"/channels", &catalog)
		results <- result{channels: catalog, err: err, kind: "channels"}
	}()

	var events eventCatalog
	var channels channelCatalog
	var eventErr, channelErr error
	for range 2 {
		res := <-results
		switch res.kind {
		case "events":
			events, eventErr = res.events, res.err
		case "channels":
			channels, channelErr = res.channels, res.err
		}
	}
	if eventErr != nil {
		timStreamsLog.Debug("live events request failed", "err", eventErr)
	}
	if channelErr != nil {
		timStreamsLog.Debug("channel catalog request failed", "err", channelErr)
	}
	if eventErr != nil && channelErr != nil {
		return nil, fmt.Errorf("timstreams search: %w", eventErr)
	}

	needle := strings.ToLower(strings.TrimSpace(query))
	genres := genreNames(events.Genres)
	if len(genres) == 0 {
		genres = genreNames(channels.Genres)
	}
	resultsFound := make([]provider.SearchResult, 0, len(events.Events)+len(channels.Channels))
	for _, item := range events.Events {
		if !validCatalogItem(item) || item.VIP || !c.hasPublicStreams(item.Streams) || !matchesEvent(item, genres, needle) {
			continue
		}
		resultsFound = append(resultsFound, toSearchResult(item, genres, true))
	}

	channelGenres := genreNames(channels.Genres)
	sort.SliceStable(channels.Channels, func(i, j int) bool {
		return strings.ToLower(channels.Channels[i].Name) < strings.ToLower(channels.Channels[j].Name)
	})
	for _, item := range channels.Channels {
		if !validCatalogItem(item) || item.VIP || !c.hasPublicStreams(item.Streams) || !matchesEvent(item, channelGenres, needle) {
			continue
		}
		resultsFound = append(resultsFound, toSearchResult(item, channelGenres, false))
	}
	if len(resultsFound) == 0 {
		return nil, provider.ErrNoResults
	}
	return resultsFound, nil
}

func (c *Client) getJSON(ctx context.Context, endpoint string, target any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("request API: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return &provider.HTTPError{Code: resp.StatusCode, URL: endpoint}
	}
	body, err := httpclient.ReadCapped(resp)
	if err != nil {
		return fmt.Errorf("read API response: %w", err)
	}
	if err := json.Unmarshal(body, target); err != nil {
		return fmt.Errorf("decode API response: %w", err)
	}
	return nil
}

func genreNames(genres []genre) map[int]genre {
	byID := make(map[int]genre, len(genres))
	for _, item := range genres {
		byID[item.ID] = item
	}
	return byID
}

func toSearchResult(item event, genres map[int]genre, isEvent bool) provider.SearchResult {
	category := genres[item.Genre]
	group := category.Name
	var names []string
	if group != "" {
		names = append(names, group)
	}
	for _, sub := range category.SubCategories {
		if sub.ID == item.SubGenre && sub.Name != "" {
			group = sub.Name
			names = append(names, sub.Name)
			break
		}
	}
	startsAt, _ := time.ParseInLocation("2006-01-02T15:04", item.Time, time.Local)
	id := handlePrefix + "event:" + item.URL
	live := false
	if !isEvent {
		id = handlePrefix + "channel:" + item.URL
		group = "Channel"
		live = true
	}
	if isEvent && !startsAt.IsZero() && !startsAt.After(time.Now()) && time.Since(startsAt) <= provider.LiveEventMaxDuration {
		live = true
	}
	if item.Flag != "" {
		names = append(names, strings.ToUpper(item.Flag))
	}
	return provider.SearchResult{
		Title:     item.Name,
		ID:        id,
		Type:      provider.ModeLive,
		MediaType: provider.MediaTypeLive,
		CoverURL:  item.Logo,
		Genres:    names,
		Group:     group,
		Live:      live,
		StartsAt:  startsAt,
	}
}

func matchesEvent(item event, genres map[int]genre, needle string) bool {
	if needle == "" {
		return true
	}
	category := genres[item.Genre]
	values := []string{item.Name, item.URL, category.Name, item.Flag}
	for _, sub := range category.SubCategories {
		if sub.ID == item.SubGenre {
			values = append(values, sub.Name)
			break
		}
	}
	for _, lane := range item.Streams {
		if !lane.VIP {
			values = append(values, lane.Name)
		}
	}
	for _, value := range values {
		if strings.Contains(strings.ToLower(value), needle) {
			return true
		}
	}
	return false
}

func validCatalogItem(item event) bool {
	return strings.TrimSpace(item.URL) != "" && strings.TrimSpace(item.Name) != ""
}

func (c *Client) hasPublicStreams(streams []stream) bool {
	for _, lane := range streams {
		if !lane.VIP && c.validShortLink(lane.URL) {
			return true
		}
	}
	return false
}

func (c *Client) validShortLink(raw string) bool {
	u, err := url.Parse(strings.TrimSpace(raw))
	base, baseErr := url.Parse(c.shortBase)
	return err == nil && baseErr == nil && u.Scheme == "https" && strings.EqualFold(u.Host, base.Host)
}

// FetchEpisodes returns the one direct playback item needed by the live flow.
func (c *Client) FetchEpisodes(_ context.Context, series provider.SearchResult) ([]provider.Episode, error) {
	if !strings.HasPrefix(series.ID, handlePrefix) {
		return nil, provider.ErrNoEpisodes
	}
	return []provider.Episode{{Title: series.Title, ID: series.ID}}, nil
}
