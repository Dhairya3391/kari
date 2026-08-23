package piratex

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"kari/internal/config"
	"kari/internal/httpclient"
	"kari/internal/lang"
	"kari/internal/logging"
	"kari/internal/provider"
	"kari/internal/util"
)

const apiBase = config.PirateXAPIBase

// Client consumes the PirateX API (a FastAPI service that scrapes
// piratexplay.cc server-side and relays every stream through its own
// /watch/{id}.m3u8 endpoint, so kari only needs slugs + episode ids and the
// returned URLs are ready to play with no headers). Search, the episode list,
// and stream resolution are three plain JSON GETs.
// Client implements provider.Provider against the Piratex API.
type Client struct {
	http        *http.Client
	searchCache *util.BoundedCache[[]provider.SearchResult]
	seriesCache *util.BoundedCache[*seriesResp]
}

// NewClient constructs the Piratex provider with the shared HTTP client.
func NewClient() (*Client, error) {
	return &Client{
		http:        httpclient.New(),
		searchCache: util.NewBoundedCache[[]provider.SearchResult](64),
		seriesCache: util.NewBoundedCache[*seriesResp](128),
	}, nil
}

// Alias implements provider.Presenter.
func (c *Client) Alias() string { return "PirateX" }

// Name implements Provider.
func (c *Client) Name() string {
	return "piratex"
}

// Modes implements Provider.
func (c *Client) Modes() []provider.Mode {
	return []provider.Mode{
		{Name: provider.ModeCartoon, Priority: 1},
	}
}

var _ provider.Provider = (*Client)(nil)

func (c *Client) get(ctx context.Context, target string) ([]byte, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, 0, fmt.Errorf("piratex request: %w", err)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("piratex request: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, fmt.Errorf("piratex read body: %w", err)
	}
	return body, resp.StatusCode, nil
}

// ── Search ───────────────────────────────────────────────────────────────────

// Search scrapes Piratex's catalogue for cartoon titles matching query.
func (c *Client) Search(ctx context.Context, query string, mode provider.ContentType) ([]provider.SearchResult, error) {
	logging.Debug("search start", "provider", c.Name(), "query", query)
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, provider.ErrNoResults
	}

	cacheKey := strings.ToLower(query)
	if cached, ok := c.searchCache.Get(cacheKey); ok {
		logging.Debug("search cached", "provider", c.Name(), "query", query)
		return cached, nil
	}

	u, err := url.Parse(apiBase + "/search")
	if err != nil {
		return nil, fmt.Errorf("piratex search: build url: %w", err)
	}
	q := u.Query()
	q.Set("q", query)
	u.RawQuery = q.Encode()

	body, status, err := c.get(ctx, u.String())
	if err != nil {
		return nil, fmt.Errorf("piratex search: %w", err)
	}
	if status != http.StatusOK {
		return nil, &provider.HTTPError{Code: status, URL: u.String()}
	}

	var resp searchResp
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("piratex search: decode response: %w", err)
	}

	results := make([]provider.SearchResult, 0, len(resp.Results))
	for _, item := range resp.Results {
		// Movies have no playable streams on piratexplay.cc — only series
		// slugs resolve to an episode list.
		if !strings.EqualFold(item.Type, "series") {
			continue
		}
		slug := strings.TrimSpace(item.Slug)
		name := strings.TrimSpace(item.Name)
		if slug == "" || name == "" {
			continue
		}
		results = append(results, provider.SearchResult{
			Title:     name,
			ID:        slug,
			Type:      provider.ModeCartoon,
			Year:      yearString(item.Year),
			MediaType: provider.MediaTypeCartoon,
		})
	}

	if len(results) == 0 {
		return nil, provider.ErrNoResults
	}

	c.searchCache.Set(cacheKey, results)
	logging.Debug("search done", "provider", c.Name(), "results", len(results))
	return results, nil
}

// ── Episodes ─────────────────────────────────────────────────────────────────

// FetchEpisodes lists episodes for a scraped series slug.
func (c *Client) FetchEpisodes(ctx context.Context, series provider.SearchResult) ([]provider.Episode, error) {
	slug := strings.TrimSpace(series.ID)
	if slug == "" {
		return nil, fmt.Errorf("piratex episodes: empty slug")
	}
	logging.Debug("fetch episodes", "provider", c.Name(), "slug", slug)

	data, err := c.fetchSeries(ctx, slug)
	if err != nil {
		return nil, err
	}

	eps := make([]provider.Episode, 0, len(data.Episodes))
	for _, e := range data.Episodes {
		id := strings.TrimSpace(e.ID)
		if id == "" || e.Number <= 0 {
			continue
		}
		season := e.Season
		if season <= 0 {
			season = 1
		}
		eps = append(eps, provider.Episode{
			Title:   fmt.Sprintf("Episode %d", e.Number),
			ID:      id,
			Season:  season,
			Episode: e.Number,
		})
	}

	if len(eps) == 0 {
		return nil, provider.ErrNoEpisodes
	}

	sort.Slice(eps, func(i, j int) bool {
		if eps[i].Season != eps[j].Season {
			return eps[i].Season < eps[j].Season
		}
		return eps[i].Episode < eps[j].Episode
	})

	logging.Debug("fetch episodes done", "provider", c.Name(), "count", len(eps))
	return eps, nil
}

// fetchSeries caches the all-seasons episode list per slug.
func (c *Client) fetchSeries(ctx context.Context, slug string) (*seriesResp, error) {
	cacheKey := strings.ToLower(slug)
	if cached, ok := c.seriesCache.Get(cacheKey); ok {
		return cached, nil
	}

	target := fmt.Sprintf("%s/series/%s", apiBase, url.PathEscape(slug))
	body, status, err := c.get(ctx, target)
	if err != nil {
		return nil, fmt.Errorf("piratex episodes: %w", err)
	}
	if status != http.StatusOK {
		return nil, &provider.HTTPError{Code: status, URL: target}
	}

	var data seriesResp
	if err := json.Unmarshal(body, &data); err != nil {
		return nil, fmt.Errorf("piratex episodes: decode response: %w", err)
	}

	c.seriesCache.Set(cacheKey, &data)
	return &data, nil
}

// ── Resolve ──────────────────────────────────────────────────────────────────

// ResolveSource picks the master HLS playlist for an episode and groups
// streams by audio language when the master exposes language variants.
func (c *Client) ResolveSource(ctx context.Context, mediaID string, episode provider.Episode) ([]provider.MediaSource, error) {
	epID := strings.TrimSpace(episode.ID)
	if epID == "" {
		// Fall back to reconstructing "{slug}-{S}x{E}" from the parts we have.
		if slug := strings.TrimSpace(mediaID); slug != "" {
			epID = fmt.Sprintf("%s-%dx%d", slug, episode.Season, episode.Episode)
		}
	}
	if epID == "" {
		return nil, fmt.Errorf("piratex resolve: empty episode id")
	}
	logging.Debug("resolve source", "provider", c.Name(), "id", epID)

	target := fmt.Sprintf("%s/watch/%s", apiBase, url.PathEscape(epID))
	body, status, err := c.get(ctx, target)
	if err != nil {
		return nil, fmt.Errorf("piratex resolve: %w", err)
	}
	switch status {
	case http.StatusNotFound:
		return nil, provider.ErrNotFound
	case http.StatusBadGateway, http.StatusGatewayTimeout, http.StatusServiceUnavailable:
		return nil, &provider.HTTPError{Code: status, URL: target}
	}
	if status != http.StatusOK {
		return nil, &provider.HTTPError{Code: status, URL: target}
	}

	var wr watchResp
	if err := json.Unmarshal(body, &wr); err != nil {
		return nil, fmt.Errorf("piratex resolve: decode response: %w", err)
	}
	if len(wr.Streams) == 0 {
		return nil, provider.ErrNoSources
	}

	// Streams come back ordered by priority (byse first when present), so rank
	// the API's default sources first.
	streams := append([]watchStream(nil), wr.Streams...)
	sort.SliceStable(streams, func(i, j int) bool {
		if streams[i].Default != streams[j].Default {
			return streams[i].Default
		}
		return streams[i].Priority < streams[j].Priority
	})

	subtitleOptions := make([]provider.SubtitleOption, 0, len(wr.Subtitles))
	for _, s := range wr.Subtitles {
		if subURL := strings.TrimSpace(s.URL); subURL != "" {
			subtitleOptions = append(subtitleOptions, provider.SubtitleOption{URL: subURL, Language: s.Language})
		}
	}

	var sources []provider.MediaSource
	seen := make(map[string]bool)
	if len(wr.Audio) > 0 {
		streamType := provider.SourceTypeHLS
		if t := strings.TrimSpace(streams[0].Type); t != "" {
			streamType = t
		}
		for _, track := range wr.Audio {
			playURL := strings.TrimSpace(track.Play)
			if playURL == "" {
				// Fall back to the default stream URL with the lang param.
				if streamURL := strings.TrimSpace(streams[0].URL); streamURL != "" && track.Language != "" {
					sep := "?"
					if strings.Contains(streamURL, "?") {
						sep = "&"
					}
					playURL = streamURL + sep + "lang=" + url.QueryEscape(track.Language)
				}
			}
			if playURL == "" {
				continue
			}

			// Prefer the normalized language name so native-script group
			// names from the master playlist (e.g. NAME="हिन्दी") render in
			// English. Raw track.Name stays a last-resort fallback for
			// non-language group labels ("Main", "5.1").
			label := strings.TrimSpace(track.Name)
			if code := lang.Normalize(track.Language); code != "" {
				label = strings.ToUpper(lang.Name(code))
			}
			if _, dup := seen[label]; dup {
				continue
			}
			seen[label] = true

			langName := ""
			if strings.TrimSpace(track.Language) != "" {
				langName = lang.Name(track.Language)
			}
			sources = append(sources, provider.MediaSource{
				URL:            playURL,
				Type:           streamType,
				Quality:        label,
				Language:       langName,
				Subtitles:      subtitleOptions,
				SuppressOrigin: true,
			})
		}
	} else {
		// No language groups on the masters (single muxed audio, e.g. Mr. Bean,
		// or byse as default) — offer the default transport as-is.
		for _, stream := range streams {
			streamURL := strings.TrimSpace(stream.URL)
			if streamURL == "" || !stream.Master {
				continue
			}
			label := transportLabel(stream.Server)
			if _, dup := seen[label]; dup {
				continue
			}
			seen[label] = true
			sources = append(sources, provider.MediaSource{
				URL:            streamURL,
				Type:           stream.Type,
				Quality:        label,
				Subtitles:      subtitleOptions,
				SuppressOrigin: true,
			})
		}
	}

	if len(sources) == 0 {
		return nil, provider.ErrNoSources
	}

	logging.Debug("resolve done", "provider", c.Name(), "sources", len(sources))
	return sources, nil
}

func transportLabel(server string) string {
	s := strings.TrimSpace(server)
	if s == "" {
		return "auto"
	}
	if strings.HasPrefix(s, "as-cdn") {
		return "cdn"
	}
	return s
}

func yearString(v any) string {
	switch t := v.(type) {
	case float64:
		if t > 0 {
			return strconv.Itoa(int(t))
		}
	case int:
		if t > 0 {
			return strconv.Itoa(t)
		}
	case string:
		if s := strings.TrimSpace(t); s != "" {
			return s
		}
	}
	return ""
}

var (
	_ provider.Provider  = (*Client)(nil)
	_ provider.Presenter = (*Client)(nil)
)
