package jellyfin

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"

	"kari/internal/httpclient"
	"kari/internal/logging"
	"kari/internal/provider"
)

type Client struct {
	http   *http.Client
	server string
	apiKey string
}

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

func (c *Client) Name() string {
	return "jellyfin"
}

func (c *Client) Modes() []provider.Mode {
	return []provider.Mode{
		{Name: provider.ModeJellyfin, Priority: 1},
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

func (c *Client) Search(ctx context.Context, query string, mode provider.ContentType) ([]provider.SearchResult, error) {
	logging.Debugf("jellyfin search start query=%q", query)
	if query == "" {
		return nil, fmt.Errorf("empty query")
	}

	u := fmt.Sprintf("%s/Search/Hints?searchTerm=%s&limit=20", c.server, url.QueryEscape(query))
	resp, err := c.authGET(ctx, u)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, &provider.HTTPError{Code: resp.StatusCode, URL: u}
	}

	var sr searchHintsResult
	if err := json.NewDecoder(resp.Body).Decode(&sr); err != nil {
		return nil, err
	}

	results := make([]provider.SearchResult, 0, len(sr.SearchHints))
	for _, h := range sr.SearchHints {
		mediaType := ""
		switch h.Type {
		case "Movie":
			mediaType = "movie"
		case "Series":
			mediaType = "tv"
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

	logging.Debugf("jellyfin search done results=%d", len(results))
	if len(results) == 0 {
		return nil, provider.ErrNoResults
	}
	return results, nil
}

func (c *Client) FetchEpisodes(ctx context.Context, series provider.SearchResult) ([]provider.Episode, error) {
	mediaID := series.ID
	logging.Debugf("jellyfin fetch episodes mediaID=%q", mediaID)

	u := fmt.Sprintf("%s/Items?parentId=%s&includeItemTypes=Episode&Recursive=true&sortBy=ParentIndexNumber,IndexNumber", c.server, mediaID)
	resp, err := c.authGET(ctx, u)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, &provider.HTTPError{Code: resp.StatusCode, URL: u}
	}

	var ir itemsResult
	if err := json.NewDecoder(resp.Body).Decode(&ir); err != nil {
		return nil, err
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

	logging.Debugf("jellyfin fetch episodes done count=%d", len(eps))
	return eps, nil
}

func (c *Client) ResolveSource(ctx context.Context, mediaID string, episode provider.Episode) ([]provider.MediaSource, error) {
	logging.Debugf("jellyfin resolve source mediaID=%q episodeID=%q", mediaID, episode.ID)

	itemID := episode.ID
	if itemID == "" {
		itemID = mediaID
	}

	streamURL := fmt.Sprintf("%s/Videos/%s/stream?static=true", c.server, itemID)

	sources := []provider.MediaSource{
		{
			URL:     streamURL,
			Quality: "Jellyfin",
			Referer: c.server + "/",
			Type:    "mp4",
		},
	}

	logging.Debugf("jellyfin resolve source done count=%d", len(sources))
	return sources, nil
}

var _ provider.Provider = (*Client)(nil)
