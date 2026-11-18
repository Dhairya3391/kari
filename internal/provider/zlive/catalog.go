package zlive

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"kari/internal/httpclient"
	"kari/internal/provider"
)

// channel is one entry of zlive.st's public channel catalog.
type channel struct {
	ID      string          `json:"id"`
	Name    string          `json:"name"`
	Tagline string          `json:"tagline"`
	Region  string          `json:"region"`
	Flag    string          `json:"flag"`
	Quality string          `json:"quality"`
	Sport   string          `json:"sport"`
	Live    bool            `json:"live"`
	Order   int             `json:"order"`
	Sources []channelSource `json:"sources"`
}

// channelSource is one playable lane advertised for a channel. The key is
// what /resolve expects in its signed payload; the label is display metadata.
type channelSource struct {
	Key     string `json:"key"`
	Label   string `json:"label"`
	Quality string `json:"-"`
}

// Search lists catalog channels matching query by name, sport, region, or
// tagline. An empty query returns the whole lineup in catalog order.
func (c *Client) Search(ctx context.Context, query string, mode provider.ContentType) ([]provider.SearchResult, error) {
	if mode != provider.ModeLive {
		return nil, provider.ErrNoResults
	}
	channels, err := c.directory(ctx)
	if err != nil {
		return nil, fmt.Errorf("zlive search: %w", err)
	}

	needle := strings.ToLower(strings.TrimSpace(query))
	results := make([]provider.SearchResult, 0, len(channels))
	for _, ch := range channels {
		if needle != "" && !channelMatches(ch, needle) {
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
// flow resolves straight from the handle, but history and the generic
// pipeline still expect an episode listing.
func (c *Client) FetchEpisodes(_ context.Context, series provider.SearchResult) ([]provider.Episode, error) {
	handle := strings.TrimSpace(series.ID)
	if handle == "" {
		return nil, provider.ErrNoEpisodes
	}
	return []provider.Episode{{Title: series.Title, ID: handle}}, nil
}

// directory returns the cached catalog, refreshing it when stale. A failed
// refresh keeps serving the previous snapshot so a transient outage does not
// empty the live tab.
func (c *Client) directory(ctx context.Context) ([]channel, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.catalog) > 0 && time.Since(c.fetchedAt) < catalogTTL {
		return c.catalog, nil
	}

	fetchCtx, cancel := context.WithTimeout(ctx, catalogFetchTimeout)
	defer cancel()
	channels, err := c.fetchCatalog(fetchCtx)
	if err != nil {
		if len(c.catalog) > 0 {
			zliveLog.Debug("catalog refresh failed; serving cached channels", "err", err)
			return c.catalog, nil
		}
		return nil, err
	}
	c.catalog = channels
	c.fetchedAt = time.Now()
	return channels, nil
}

// fetchCatalog downloads and decodes the public channel list, retrying
// transient connection resets within the refresh budget.
func (c *Client) fetchCatalog(ctx context.Context) ([]channel, error) {
	var lastErr error
	for attempt := 0; attempt < catalogAttempts; attempt++ {
		channels, err := c.fetchCatalogOnce(ctx)
		if err == nil {
			return channels, nil
		}
		lastErr = err
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
	}
	if lastErr == nil {
		lastErr = provider.ErrUpstreamChanged
	}
	return nil, lastErr
}

// fetchCatalogOnce performs one catalog request and decodes its payload,
// keeping only entries that can actually be resolved.
func (c *Client) fetchCatalogOnce(ctx context.Context) ([]channel, error) {
	requestCtx, cancel := context.WithTimeout(ctx, catalogAttemptTimeout)
	defer cancel()
	target := c.apiBase + "/channels.json"
	req, err := http.NewRequestWithContext(requestCtx, http.MethodGet, target, nil)
	if err != nil {
		return nil, fmt.Errorf("create catalog request: %w", err)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch catalog: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, &provider.HTTPError{Code: resp.StatusCode, URL: target}
	}
	body, err := httpclient.ReadCapped(resp)
	if err != nil {
		return nil, fmt.Errorf("read catalog: %w", err)
	}

	var entries []channel
	if err := json.Unmarshal(body, &entries); err != nil {
		return nil, fmt.Errorf("decode catalog: %w", err)
	}
	channels := make([]channel, 0, len(entries))
	for _, entry := range entries {
		if entry.ID == "" || entry.Name == "" || len(playableSources(entry)) == 0 {
			continue
		}
		channels = append(channels, entry)
	}
	if len(channels) == 0 {
		return nil, fmt.Errorf("catalog has no playable channels: %w", provider.ErrUpstreamChanged)
	}
	sort.SliceStable(channels, func(i, j int) bool {
		if channels[i].Order != channels[j].Order {
			return channels[i].Order < channels[j].Order
		}
		return strings.ToLower(channels[i].Name) < strings.ToLower(channels[j].Name)
	})
	return channels, nil
}

// playableSources returns the channel's resolution keys, deduplicated in
// declaration order.
func playableSources(ch channel) []channelSource {
	sources := make([]channelSource, 0, len(ch.Sources))
	seen := make(map[string]bool, len(ch.Sources))
	for _, source := range ch.Sources {
		key := strings.TrimSpace(source.Key)
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		label := strings.TrimSpace(source.Label)
		if label == "" {
			label = "zlive"
		}
		sources = append(sources, channelSource{Key: key, Label: label, Quality: ch.Quality})
	}
	return sources
}

// channelMatches reports whether the lowercased query names the channel, its
// category, region, or tagline.
func channelMatches(ch channel, needle string) bool {
	return strings.Contains(strings.ToLower(ch.Name), needle) ||
		strings.Contains(strings.ToLower(ch.Sport), needle) ||
		strings.Contains(strings.ToLower(ch.Region), needle) ||
		strings.Contains(strings.ToLower(ch.Tagline), needle) ||
		strings.Contains(strings.ToLower(ch.ID), needle)
}

// searchResult maps one catalog channel to a live search result.
func searchResult(ch channel) provider.SearchResult {
	group := strings.TrimSpace(ch.Sport)
	if group == "" {
		group = "Live TV"
	}
	var genres []string
	if region := strings.TrimSpace(ch.Region); region != "" {
		genres = append(genres, region)
	}
	return provider.SearchResult{
		Title:     ch.Name,
		ID:        channelHandle(ch.ID),
		Type:      provider.ModeLive,
		MediaType: provider.MediaTypeLive,
		Live:      true,
		Overview:  strings.TrimSpace(ch.Tagline),
		Genres:    genres,
		Group:     group,
	}
}

// channelHandle builds the provider-specific handle stored in search results
// and history.
func channelHandle(id string) string {
	return handlePrefix + id
}

// parseHandle extracts a channel ID from a provider handle, accepting a bare
// ID for older entries.
func parseHandle(handle string) (string, bool) {
	id := strings.TrimPrefix(strings.TrimSpace(handle), handlePrefix)
	return id, id != ""
}
