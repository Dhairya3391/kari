package pengu

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"kari/internal/logging"
	"kari/internal/provider"
	"kari/internal/provider/kit"
)

// liveHandlePrefix marks a live catalog item. VOD handles are bare TMDB
// numbers, so the prefix is also how ResolveSource tells the two apart.
const liveHandlePrefix = "pp-live:"

// liveCatalogPrefix marks live catalogs in the manifest ("pp-live-now").
const liveCatalogPrefix = "pp-live-"

const (
	// liveFetchTimeout bounds one full manifest + catalog sweep. It is
	// detached from the caller's search budget because every concurrent
	// search shares the same refresh.
	liveFetchTimeout = 20 * time.Second
	// liveCacheTTL keeps the live snapshot fresh without refetching dozens of
	// catalogs on every keystroke. Live events change slowly relative to a
	// search session.
	liveCacheTTL = 90 * time.Second
	// liveCatalogWorkers caps the catalog fan-out. The upstream starts
	// shedding connections above ~15 parallel requests.
	liveCatalogWorkers = 12
)

// liveManifest is the catalog portion of the addon manifest.
type liveManifest struct {
	Catalogs []liveCatalog `json:"catalogs"`
}

// liveCatalog is one manifest catalog entry.
type liveCatalog struct {
	ID   string `json:"id"`
	Type string `json:"type"`
	Name string `json:"name"`
}

// liveCatalogPayload is one catalog response.
type liveCatalogPayload struct {
	Metas []liveMeta `json:"metas"`
}

// liveMeta is one live event or channel as the catalog publishes it.
type liveMeta struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Poster      string   `json:"poster"`
	Background  string   `json:"background"`
	Description string   `json:"description"`
	Genres      []string `json:"genres"`
	Released    string   `json:"released"`
}

// liveEntry is one search result plus the lowercase text used to match it.
type liveEntry struct {
	result   provider.SearchResult
	haystack string
}

// liveCache is the shared live snapshot. Only live.mu, live.entries and
// live.fetchedAt mutate; refreshing keeps background fills single-flight.
type liveCache struct {
	mu         sync.Mutex
	entries    []liveEntry
	fetchedAt  time.Time
	refreshing bool
}

// fresh returns the snapshot while it is within its TTL.
func (c *liveCache) fresh() ([]liveEntry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.entries) == 0 || time.Since(c.fetchedAt) >= liveCacheTTL {
		return nil, false
	}
	return c.entries, true
}

// stale returns the snapshot regardless of age so a failed refresh does not
// empty the live tab.
func (c *liveCache) stale() ([]liveEntry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.entries) == 0 {
		return nil, false
	}
	return c.entries, true
}

// store replaces the snapshot.
func (c *liveCache) store(entries []liveEntry) {
	c.mu.Lock()
	c.entries = entries
	c.fetchedAt = time.Now()
	c.mu.Unlock()
}

// beginRefresh claims the background refresh slot.
func (c *liveCache) beginRefresh() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.refreshing {
		return false
	}
	c.refreshing = true
	return true
}

// endRefresh releases the background refresh slot.
func (c *liveCache) endRefresh() {
	c.mu.Lock()
	c.refreshing = false
	c.mu.Unlock()
}

// searchLive filters the live snapshot by title, group, genre, or description.
// An empty query returns the whole snapshot, which is what makes the live tab
// browsable.
func (c *Client) searchLive(ctx context.Context, query string) ([]provider.SearchResult, error) {
	entries, err := c.liveEntries(ctx)
	if err != nil {
		return nil, fmt.Errorf("pengu live search: %w", err)
	}
	needle := strings.ToLower(strings.TrimSpace(query))
	results := make([]provider.SearchResult, 0, len(entries))
	for _, entry := range entries {
		if needle != "" && !strings.Contains(entry.haystack, needle) {
			continue
		}
		results = append(results, entry.result)
	}
	if len(results) == 0 {
		return nil, provider.ErrNoResults
	}
	return results, nil
}

// liveEntries returns the live snapshot. A stale snapshot is served
// immediately while it refreshes in the background. With no snapshot yet, only
// the core catalogs are fetched synchronously so the first live screen renders
// inside the search grace window; the full catalog set follows in the
// background and is used by the next search.
func (c *Client) liveEntries(ctx context.Context) ([]liveEntry, error) {
	if entries, ok := c.live.fresh(); ok {
		return entries, nil
	}
	if entries, ok := c.live.stale(); ok {
		c.refreshLiveAsync()
		return entries, nil
	}

	// These stable IDs cover live events, upcoming events and 24/7 channels.
	// The slow manifest is skipped so the first search stays in the grace
	// window; the detached refresh fetches the full catalog set.
	entries, err := c.collectLiveCatalogs(ctx, []liveCatalog{
		{ID: "pp-live-now", Type: "tv", Name: "Live Now - TV"},
		{ID: "pp-live-upcoming", Type: "tv", Name: "Upcoming - TV"},
		{ID: "pp-live-channels", Type: "tv", Name: "Channels - TV"},
	})
	if err != nil {
		return nil, err
	}
	c.live.store(entries)
	c.refreshLiveAsync()
	return entries, nil
}

// refreshLiveAsync fills the full snapshot without blocking a search. A
// failed refresh leaves the quick snapshot in place.
func (c *Client) refreshLiveAsync() {
	if !c.live.beginRefresh() {
		return
	}
	go func() {
		defer c.live.endRefresh()
		ctx, cancel := context.WithTimeout(context.Background(), liveFetchTimeout)
		defer cancel()
		entries, err := c.fetchLiveEntries(ctx)
		if err != nil {
			logging.Debug("pengu live background refresh failed", "err", err)
			return
		}
		c.live.store(entries)
	}()
}

// fetchLiveEntries reads every live catalog the manifest advertises. The
// manifest is fetched first so new PenguPlay categories appear without a Kari
// release, and catalogs are collected in manifest order (Live Now first).
func (c *Client) fetchLiveEntries(ctx context.Context) ([]liveEntry, error) {
	var manifest liveManifest
	if err := c.getJSON(ctx, c.apiBase+"/manifest.json", &manifest); err != nil {
		return nil, fmt.Errorf("live manifest: %w", err)
	}

	catalogs := make([]liveCatalog, 0, len(manifest.Catalogs))
	for _, catalog := range manifest.Catalogs {
		if catalog.Type != "tv" || !strings.HasPrefix(catalog.ID, liveCatalogPrefix) {
			continue
		}
		catalogs = append(catalogs, catalog)
	}
	if len(catalogs) == 0 {
		return nil, fmt.Errorf("manifest advertises no live catalogs: %w", provider.ErrUpstreamChanged)
	}
	return c.collectLiveCatalogs(ctx, catalogs)
}

// collectLiveCatalogs fetches the given catalogs in parallel, preserving their
// order, and dedupes items that appear in more than one catalog.
func (c *Client) collectLiveCatalogs(ctx context.Context, catalogs []liveCatalog) ([]liveEntry, error) {
	perCatalog := make([][]liveEntry, len(catalogs))
	jobs := make(chan int)
	var wg sync.WaitGroup
	for i := 0; i < liveCatalogWorkers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for index := range jobs {
				entries, err := c.fetchLiveCatalog(ctx, catalogs[index])
				if err != nil {
					// One broken category must not hide the others.
					logging.Debug("pengu live catalog failed", "catalog", catalogs[index].ID, "err", err)
					continue
				}
				perCatalog[index] = entries
			}
		}()
	}
	for index := range catalogs {
		jobs <- index
	}
	close(jobs)
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	entries := make([]liveEntry, 0, 800)
	seen := make(map[string]bool)
	for _, catalogEntries := range perCatalog {
		for _, entry := range catalogEntries {
			if seen[entry.result.ID] {
				continue
			}
			seen[entry.result.ID] = true
			entries = append(entries, entry)
		}
	}
	if len(entries) == 0 {
		logging.Debug("pengu live catalogs returned no items")
	}
	return entries, nil
}

// fetchLiveCatalog reads one catalog and maps every meta to a live result.
func (c *Client) fetchLiveCatalog(ctx context.Context, catalog liveCatalog) ([]liveEntry, error) {
	endpoint := c.apiBase + "/catalog/tv/" + url.PathEscape(catalog.ID) + ".json"
	var payload liveCatalogPayload
	if err := c.getJSON(ctx, endpoint, &payload); err != nil {
		return nil, err
	}
	group, channel := liveCatalogGroup(catalog)
	entries := make([]liveEntry, 0, len(payload.Metas))
	for _, meta := range payload.Metas {
		if strings.TrimSpace(meta.ID) == "" || strings.TrimSpace(meta.Name) == "" {
			continue
		}
		entries = append(entries, liveEntry{
			result:   liveResult(meta, group, channel),
			haystack: liveHaystack(meta, group),
		})
	}
	return entries, nil
}

// liveResult maps one catalog meta to the search result shape the UI expects.
// Channel catalogs are always-on, so they carry Group "Channel" and no start
// time; event catalogs use the kickoff timestamp to land in the guide's Live
// now or Upcoming section.
func liveResult(meta liveMeta, group string, channel bool) provider.SearchResult {
	result := provider.SearchResult{
		Title:     strings.TrimSpace(meta.Name),
		ID:        meta.ID,
		Type:      provider.ModeLive,
		MediaType: provider.MediaTypeLive,
		Group:     group,
		CoverURL:  strings.TrimSpace(meta.Poster),
		Overview:  strings.TrimSpace(meta.Description),
		Genres:    meta.Genres,
	}
	if result.CoverURL == "" {
		result.CoverURL = strings.TrimSpace(meta.Background)
	}
	if result.Group == "" && len(result.Genres) > 0 {
		result.Group = result.Genres[0]
	}
	if channel {
		result.Live = true
		return result
	}
	if released, err := time.Parse(time.RFC3339, strings.TrimSpace(meta.Released)); err == nil {
		start := kit.LocalStartsAt(released)
		result.StartsAt = start
		// Match the guide's own event window so the LIVE label and the
		// section a row lands in cannot disagree.
		result.Live = !start.After(time.Now()) && time.Since(start) <= provider.LiveEventMaxDuration
	} else {
		result.Live = true
	}
	return result
}

// liveHaystack builds the lowercase text a query is matched against.
func liveHaystack(meta liveMeta, group string) string {
	parts := []string{meta.Name, group, meta.Description}
	parts = append(parts, meta.Genres...)
	return strings.ToLower(strings.Join(parts, " "))
}

// liveCatalogGroup returns the UI group for a catalog and whether every item
// in it is an always-on channel. PenguTV team/category feeds and the channels
// catalog are 24/7 streams, not scheduled events.
func liveCatalogGroup(catalog liveCatalog) (string, bool) {
	if catalog.ID == "pp-live-channels" || strings.HasPrefix(catalog.ID, "pp-live-pengutv-") {
		return "Channel", true
	}
	// The catch-all feeds mix every sport, so their rows fall back to the
	// item's own genre for the category heading instead of a useless
	// "Live Now" label repeated under the section title.
	if catalog.ID == "pp-live-now" || catalog.ID == "pp-live-upcoming" {
		return "", false
	}
	return liveGroup(catalog.Name), false
}

// liveGroup turns "Soccer - TV (PenguTV)" into "Soccer" for the UI's group
// label.
func liveGroup(name string) string {
	group := strings.TrimSpace(name)
	group = strings.TrimSuffix(group, "(PenguTV)")
	group = strings.TrimSuffix(strings.TrimSpace(group), "- TV")
	return strings.TrimSpace(group)
}

// resolveLive resolves one live catalog item through the addon's tv stream
// resource.
func (c *Client) resolveLive(ctx context.Context, mediaID string, episode provider.Episode) ([]provider.MediaSource, error) {
	handle := strings.TrimSpace(mediaID)
	if !isLiveHandle(handle) {
		handle = strings.TrimSpace(episode.ID)
	}
	if !isLiveHandle(handle) {
		return nil, fmt.Errorf("pengu: invalid live handle %q: %w", mediaID, provider.ErrNoSources)
	}

	endpoint := c.apiBase + "/stream/tv/" + url.PathEscape(handle) + ".json"
	items, err := c.fetchStreams(ctx, endpoint)
	if err != nil {
		return nil, err
	}
	sources := provider.FilterDirectSources(c.buildSources(items))
	if len(sources) == 0 {
		return nil, provider.ErrNoSources
	}
	logging.Debug("pengu live resolve done", "provider", c.Name(), "handle", handle, "sources", len(sources))
	return sources, nil
}

// VerifyLiveStream implements provider.LiveStreamVerifier so dead live
// mirrors are dropped before playback.
func (c *Client) VerifyLiveStream(ctx context.Context, mode provider.ContentType, source provider.MediaSource) error {
	if mode != provider.ModeLive {
		return nil
	}
	return provider.VerifyLiveStream(ctx, c.httpClient, source)
}

// isLiveHandle reports whether an ID names a live catalog item.
func isLiveHandle(id string) bool {
	return strings.HasPrefix(strings.TrimSpace(id), liveHandlePrefix)
}

// getJSON fetches and decodes one JSON resource.
func (c *Client) getJSON(ctx context.Context, endpoint string, dst any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return provider.WrapHint("pengu", "catalog", provider.KindUpstream, "catalog request failed", err)
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusUnauthorized, http.StatusForbidden:
		return provider.WrapHint("pengu", "catalog", provider.KindAuthRequired,
			"PenguPlay rejected the bundled access token",
			&provider.HTTPError{Code: resp.StatusCode})
	default:
		// endpoint carries the bundled token in its path; never attach it to
		// an error that could reach a log or a user-facing message.
		return &provider.HTTPError{Code: resp.StatusCode}
	}
	if err := json.NewDecoder(resp.Body).Decode(dst); err != nil {
		return fmt.Errorf("decode: %w", err)
	}
	return nil
}
