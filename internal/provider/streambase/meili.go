package streambase

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sync/singleflight"

	"kari/internal/config"
	"kari/internal/httpclient"
	"kari/internal/provider"
	"kari/internal/util"
)

// Result is one raw hit from the meilisearch TMDB index.
type Result struct {
	TMDBID        int    `json:"tmdb_id"`
	Title         string `json:"title"`
	OriginalTitle string `json:"original_title"`
	MediaType     string `json:"media_type"`
	Year          any    `json:"year"`
	EpisodeCount  *int   `json:"episode_count"`
	Language      string `json:"original_language"`
}

// Response wraps a meilisearch reply including any corrected query.
type Response struct {
	Query          string   `json:"query"`
	CorrectedQuery string   `json:"corrected_query,omitempty"`
	Total          int      `json:"total"`
	Results        []Result `json:"results"`
}

var (
	sharedMeiliCache = util.NewBoundedCache[meiliCacheEntry](200)
	sharedMeiliSF    singleflight.Group
)

type meiliCacheEntry struct {
	results  []provider.SearchResult
	cachedAt time.Time
}

// Client queries the shared TMDB search index used by TMDB-keyed providers.
type Client struct {
	httpClient *http.Client
}

// NewClient constructs the search client with the desktop UA.
func NewClient() *Client {
	return &Client{
		httpClient: httpclient.NewWithUserAgent(config.DesktopUserAgent),
	}
}
// NewClientWithHTTPClient constructs the search client over a custom
// HTTP client (offline tests serve canned index payloads).
func NewClientWithHTTPClient(hc *http.Client) *Client {
	return &Client{httpClient: hc}
}

// SearchWithMode queries the TMDB search index for the given content mode.
// ModeMovies and ModeTV hit their dedicated endpoints; every other mode
// uses the generic multi-search endpoint.
func (c *Client) SearchWithMode(ctx context.Context, query string, mode provider.ContentType) ([]provider.SearchResult, error) {
	endpoint := "/search"
	switch mode {
	case provider.ModeMovies:
		endpoint = "/movies"
	case provider.ModeTV:
		endpoint = "/tv"
	}
	return c.SearchWithEndpoint(ctx, query, endpoint)
}

// SearchWithEndpoint performs the raw query against one endpoint and maps
// hits to provider results keyed by TMDB id.
func (c *Client) SearchWithEndpoint(ctx context.Context, query string, endpoint string) ([]provider.SearchResult, error) {
	normalizedQ := strings.TrimSpace(query)
	if normalizedQ == "" {
		return nil, fmt.Errorf("empty query")
	}

	cacheKey := fmt.Sprintf("%s:%s", endpoint, strings.ToLower(normalizedQ))
	if entry, ok := sharedMeiliCache.Get(cacheKey); ok && time.Since(entry.cachedAt) < 10*time.Minute {
		return entry.results, nil
	}

	res, err, _ := sharedMeiliSF.Do(cacheKey, func() (any, error) {
		if entry, ok := sharedMeiliCache.Get(cacheKey); ok && time.Since(entry.cachedAt) < 10*time.Minute {
			return entry.results, nil
		}

		out, fetchErr := c.searchDirect(ctx, normalizedQ, endpoint)
		if fetchErr != nil {
			return nil, fetchErr
		}
		if len(out) > 0 {
			sharedMeiliCache.Set(cacheKey, meiliCacheEntry{
				results:  out,
				cachedAt: time.Now(),
			})
		}
		return out, nil
	})
	if err != nil {
		return nil, err
	}
	return res.([]provider.SearchResult), nil
}

func (c *Client) searchDirect(ctx context.Context, query string, endpoint string) ([]provider.SearchResult, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", config.SearchAPIBase+endpoint, nil)
	if err != nil {
		return nil, err
	}
	q := req.URL.Query()
	q.Set("q", query)
	q.Set("limit", "30")
	req.URL.RawQuery = q.Encode()

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("API returned status %d", resp.StatusCode)
	}

	var result Response
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}
	if len(result.Results) == 0 {
		return nil, provider.ErrNoResults
	}

	out := make([]provider.SearchResult, 0, len(result.Results))
	for _, r := range result.Results {
		yearStr := ""
		if r.Year != nil {
			yearStr = fmt.Sprintf("%v", r.Year)
		}
		out = append(out, provider.SearchResult{
			Title:     r.Title,
			ID:        strconv.Itoa(r.TMDBID),
			MediaType: r.MediaType,
			Year:      yearStr,
			TMDBID:    r.TMDBID,
		})
	}

	return out, nil
}
