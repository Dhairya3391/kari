package jellyfin

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"kari/internal/httpclient"
	"kari/internal/logging"
	"kari/internal/provider"
)

// log scopes every line from this package with its identity.
var jellyLog = logging.With("provider", "jellyfin")

// Client implements provider.Provider against the Jellyfin API.
type Client struct {
	http   *http.Client
	server string
	apiKey string

	mu        sync.Mutex
	library   []provider.SearchResult
	libraryAt time.Time
}

// NewClient validates credentials and constructs the Jellyfin provider.
func NewClient(server, apiKey string) (*Client, error) {
	if server == "" || apiKey == "" {
		return nil, fmt.Errorf("jellyfin server URL and API key are required")
	}
	server = strings.TrimRight(server, "/")
	return &Client{
		http:   httpclient.New(),
		server: server,
		apiKey: apiKey,
	}, nil
}

// Alias implements provider.Presenter.
func (c *Client) Alias() string { return "Jellyfin" }

// Name implements Provider.
func (c *Client) Name() string {
	return "jellyfin"
}

// Modes implements Provider.
func (c *Client) Modes() []provider.Mode {
	return []provider.Mode{
		{Name: provider.ModeJellyfin, Priority: 1},
	}
}

// Features implements provider.FeatureSource. Jellyfin searches match
// against the server library: an empty query browses it, results change
// server-side so they must not be cached.
func (c *Client) Features(mode provider.ContentType) provider.Features {
	if mode != provider.ModeJellyfin {
		return provider.Features{}
	}
	return provider.Features{
		AllowEmptyQuery:   true,
		NoCachedSearches:  true,
		SearchPlaceholder: "Search… (Enter on empty = browse library)",
	}
}

func (c *Client) authGET(ctx context.Context, url string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Emby-Token", c.apiKey)
	return c.http.Do(req)
}

// Search ranks the cached server library against query; an empty query
// browses everything. Falls back to server-side hints when the library
// listing fails.
func (c *Client) Search(ctx context.Context, query string, mode provider.ContentType) ([]provider.SearchResult, error) {
	logging.Debug("search start", "provider", c.Name(), "query", query)

	// Match against the cached library so short and partial queries work;
	// an empty query browses the whole library.
	library, err := c.getLibrary(ctx)
	if err != nil {
		jellyLog.Debug("library fetch failed; falling back to server search", "err", err)
		return c.searchHints(ctx, query)
	}

	results := rankLibrary(library, query)
	logging.Debug("search done", "provider", c.Name(), "results", len(results))
	if len(results) == 0 {
		return nil, provider.ErrNoResults
	}
	return results, nil
}

// searchHints queries the server-side /Search/Hints endpoint. Used as a
// fallback when the library listing is unavailable.
func (c *Client) searchHints(ctx context.Context, query string) ([]provider.SearchResult, error) {
	if query == "" {
		return nil, fmt.Errorf("empty query")
	}

	u := fmt.Sprintf("%s/Search/Hints?searchTerm=%s&limit=20", c.server, url.QueryEscape(query))
	resp, err := c.authGET(ctx, u)
	if err != nil {
		return nil, fmt.Errorf("jellyfin search hints: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, &provider.HTTPError{Code: resp.StatusCode, URL: u}
	}

	var sr searchHintsResult
	if err := json.NewDecoder(resp.Body).Decode(&sr); err != nil {
		return nil, fmt.Errorf("jellyfin search hints: decode response: %w", err)
	}

	results := make([]provider.SearchResult, 0, len(sr.SearchHints))
	for _, h := range sr.SearchHints {
		mediaType := ""
		switch h.Type {
		case "Movie":
			mediaType = provider.MediaTypeMovie
		case "Series":
			mediaType = provider.MediaTypeTV
		default:
			continue
		}

		title := h.Name
		if h.Series != "" {
			title = h.Series
		}

		id := h.ItemID
		if id == "" {
			continue
		}

		if h.SeriesID != "" {
			id = h.SeriesID
		}

		year := ""
		if h.ProductionYear > 0 {
			year = fmt.Sprintf("%d", h.ProductionYear)
		}

		results = append(results, provider.SearchResult{
			Title:     title,
			ID:        id,
			Type:      provider.ModeJellyfin,
			Year:      year,
			MediaType: mediaType,
		})
	}

	logging.Debug("search done", "provider", c.Name(), "results", len(results))
	if len(results) == 0 {
		return nil, provider.ErrNoResults
	}
	return results, nil
}

// FetchEpisodes lists episodes of one Jellyfin series item.
func (c *Client) FetchEpisodes(ctx context.Context, series provider.SearchResult) ([]provider.Episode, error) {
	mediaID := series.ID
	logging.Debug("fetch episodes", "provider", c.Name(), "mediaID", mediaID)

	u := fmt.Sprintf("%s/Items?parentId=%s&includeItemTypes=Episode&Recursive=true&sortBy=ParentIndexNumber,IndexNumber", c.server, mediaID)
	resp, err := c.authGET(ctx, u)
	if err != nil {
		return nil, fmt.Errorf("jellyfin fetch episodes: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, &provider.HTTPError{Code: resp.StatusCode, URL: u}
	}

	var ir itemsResult
	if err := json.NewDecoder(resp.Body).Decode(&ir); err != nil {
		return nil, fmt.Errorf("jellyfin fetch episodes: decode response: %w", err)
	}

	eps := make([]provider.Episode, 0, len(ir.Items))
	for _, it := range ir.Items {
		eps = append(eps, provider.Episode{
			Title:   it.Name,
			ID:      it.ID,
			Episode: it.EpisodeNumber,
			Season:  it.SeasonNumber,
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

// ResolveSource builds a direct stream URL for the episode item (or the
// series item itself for movies).
func (c *Client) ResolveSource(ctx context.Context, mediaID string, episode provider.Episode) ([]provider.MediaSource, error) {
	logging.Debug("resolve source", "provider", c.Name(), "mediaID", mediaID, "episodeID", episode.ID)

	itemID := episode.ID
	if itemID == "" {
		itemID = mediaID
	}

	streamURL := fmt.Sprintf("%s/Videos/%s/stream?static=true&api_key=%s", c.server, itemID, url.QueryEscape(c.apiKey))

	sources := []provider.MediaSource{
		{
			URL:     streamURL,
			Quality: "Jellyfin",
			Referer: c.server + "/",
			Type:    provider.SourceTypeMP4,
		},
	}

	logging.Debug("resolve source done", "provider", c.Name(), "count", len(sources))
	return sources, nil
}

var (
	_ provider.Provider      = (*Client)(nil)
	_ provider.Presenter     = (*Client)(nil)
	_ provider.FeatureSource = (*Client)(nil)
)
