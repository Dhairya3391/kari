package scrobble

// One-way watch-history import: AniList and Trakt act as the source of
// truth for what was watched elsewhere, and the local history store
// absorbs it. Nothing here ever writes back to the trackers and nothing
// overwrites local progress — see service.ImportWatched for the merge
// policy. Fetchers translate each tracker's list shape into WatchedItem;
// the merge layer expands and dedupes them.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"kari/internal/config"
	"kari/internal/provider"
)

// WatchedItem is one remotely-watched unit: an episode, a movie, one
// step of an anime progress range (the AniList fetcher expands ranges
// into per-episode items so the merge layer stays dumb), or one manga
// series position (Chapter carries the progress chapter; chapters are
// fractional strings, so ranges are never expanded for manga).
type WatchedItem struct {
	Title     string
	Mode      string // provider.Mode* vocabulary
	MediaType string // provider.MediaType* vocabulary
	Season    int
	Episode   int
	Chapter   string // manga progress chapter ("12", "12.5"); else ""
	TMDBID    int
	AniListID int // AniList catalog id for anime/manga; else 0
	Complete  bool
	WatchedAt time.Time
}

// apiBase overrides the production endpoint for tests (httptest servers).
// Production constructors leave it empty, which selects the config const.
func (c *AniListClient) endpoint() string {
	if c.apiBase != "" {
		return c.apiBase
	}
	return config.AniListAPIBase
}

// apiBase overrides the production endpoint for tests (httptest servers).
func (c *TraktClient) endpoint() string {
	if c.apiBase != "" {
		return c.apiBase
	}
	return config.TraktAPIBase
}

// FetchWatchedList returns the viewer's anime lists as per-episode watch
// items. AniList tracks a single progress number per title, so a title at
// progress N expands to N items (episodes 1..N-1 complete, N complete
// only when the list status is COMPLETED). Titles with nothing watched
// and non-watch statuses (PLANNING, DROPPED, PAUSED) are skipped.
// Large lists arrive in chunks (perChunk, max 500); chunks are followed
// via hasNextChunk so nothing is silently truncated.
func (c *AniListClient) FetchWatchedList(ctx context.Context) ([]WatchedItem, error) {
	if !c.IsAuthenticated() {
		return nil, fmt.Errorf("anilist not connected")
	}

	userID, err := c.viewerID(ctx)
	if err != nil {
		return nil, err
	}

	const perChunk = 500
	const maxChunks = 25 // 12,500 entries; the API caps at 11,000 anyway
	var out []WatchedItem
	for chunk := 1; chunk <= maxChunks; chunk++ {
		entries, hasNext, err := c.fetchListChunk(ctx, userID, chunk, perChunk, "ANIME")
		if err != nil {
			return nil, err
		}
		for _, e := range entries {
			out = append(out, expandAnilistEntry(e)...)
		}
		if !hasNext {
			break
		}
	}
	return out, nil
}

// FetchMangaList returns the viewer's manga lists as one position item
// per title: AniList manga progress is a chapter count, and chapter
// numbers are fractional strings providers don't share, so ranges are
// never expanded — the merge layer records the position and reads
// forward from it.
func (c *AniListClient) FetchMangaList(ctx context.Context) ([]WatchedItem, error) {
	if !c.IsAuthenticated() {
		return nil, fmt.Errorf("anilist not connected")
	}

	userID, err := c.viewerID(ctx)
	if err != nil {
		return nil, err
	}

	const perChunk = 500
	const maxChunks = 25
	var out []WatchedItem
	for chunk := 1; chunk <= maxChunks; chunk++ {
		entries, hasNext, err := c.fetchListChunk(ctx, userID, chunk, perChunk, "MANGA")
		if err != nil {
			return nil, err
		}
		for _, e := range entries {
			if item, ok := expandMangaEntry(e); ok {
				out = append(out, item)
			}
		}
		if !hasNext {
			break
		}
	}
	return out, nil
}

// expandMangaEntry turns one manga list entry into a single position
// item. Progress is chapters read; the reader resumes at page one of
// that chapter (or marks the series finished when completed).
func expandMangaEntry(e anilistListEntry) (WatchedItem, bool) {
	switch e.Status {
	case "CURRENT", "COMPLETED", "REPEATING":
	default:
		return WatchedItem{}, false
	}
	title := strings.TrimSpace(e.Media.Title.English)
	if title == "" {
		title = strings.TrimSpace(e.Media.Title.Romaji)
	}
	if title == "" {
		return WatchedItem{}, false
	}
	completed := e.Status == "COMPLETED"
	chapter := ""
	if e.Progress > 0 {
		chapter = strconv.Itoa(e.Progress)
	}
	if chapter == "" && !completed {
		return WatchedItem{}, false
	}
	watchedAt := time.Now()
	if e.UpdatedAt > 0 {
		watchedAt = time.Unix(e.UpdatedAt, 0)
	}
	return WatchedItem{
		Title:     title,
		Mode:      string(provider.ModeManga),
		MediaType: provider.MediaTypeManga,
		Chapter:   chapter,
		AniListID: e.Media.ID,
		Complete:  completed,
		WatchedAt: watchedAt,
	}, true
}

// fetchListChunk pulls one MediaListCollection chunk for an anime or
// manga list: the entries plus whether another chunk follows.
func (c *AniListClient) fetchListChunk(ctx context.Context, userID, chunk, perChunk int, listType string) ([]anilistListEntry, bool, error) {
	query := `
	query ($userId: Int, $chunk: Int, $perChunk: Int, $type: MediaType) {
		MediaListCollection(userId: $userId, type: $type, status_in: [CURRENT, COMPLETED, REPEATING], chunk: $chunk, perChunk: $perChunk) {
			hasNextChunk
			lists {
				entries {
					status
					progress
					updatedAt
					media {
						id
						title { english romaji }
						episodes
						format
					}
				}
			}
		}
	}
	`
	var res struct {
		Data struct {
			Collection struct {
				HasNextChunk bool `json:"hasNextChunk"`
				Lists        []struct {
					Entries []anilistListEntry `json:"entries"`
				} `json:"lists"`
			} `json:"MediaListCollection"`
		} `json:"data"`
	}
	vars := map[string]interface{}{"userId": userID, "chunk": chunk, "perChunk": perChunk, "type": listType}
	if err := c.doGraphQL(ctx, query, vars, &res); err != nil {
		return nil, false, fmt.Errorf("anilist watch list: %w", err)
	}
	var out []anilistListEntry
	for _, list := range res.Data.Collection.Lists {
		out = append(out, list.Entries...)
	}
	return out, res.Data.Collection.HasNextChunk, nil
}

type anilistListEntry struct {
	Status    string `json:"status"`
	Progress  int    `json:"progress"`
	UpdatedAt int64  `json:"updatedAt"`
	Media     struct {
		ID    int `json:"id"`
		Title struct {
			English string `json:"english"`
			Romaji  string `json:"romaji"`
		} `json:"title"`
		Episodes *int   `json:"episodes"`
		Format   string `json:"format"`
	} `json:"media"`
}

// viewerID resolves the token owner's AniList user id, which scopes the
// list collection query.
func (c *AniListClient) viewerID(ctx context.Context) (int, error) {
	var res struct {
		Data struct {
			Viewer struct {
				ID int `json:"id"`
			} `json:"Viewer"`
		} `json:"data"`
	}
	if err := c.doGraphQL(ctx, "query { Viewer { id } }", nil, &res); err != nil {
		return 0, fmt.Errorf("anilist viewer: %w", err)
	}
	if res.Data.Viewer.ID == 0 {
		return 0, fmt.Errorf("anilist viewer: unknown user")
	}
	return res.Data.Viewer.ID, nil
}

// expandAnilistEntry turns one list entry into per-episode watch items.
// Only genuinely watched episodes are emitted: episodes past the progress
// number are never invented, and entries with nothing watched are skipped.
func expandAnilistEntry(e anilistListEntry) []WatchedItem {
	switch e.Status {
	case "CURRENT", "COMPLETED", "REPEATING":
	default:
		return nil
	}
	title := strings.TrimSpace(e.Media.Title.English)
	if title == "" {
		title = strings.TrimSpace(e.Media.Title.Romaji)
	}
	if title == "" {
		return nil
	}

	total := 0
	if e.Media.Episodes != nil {
		total = *e.Media.Episodes
	}
	watchedUpTo := e.Progress
	completed := e.Status == "COMPLETED"
	if completed && total > watchedUpTo {
		watchedUpTo = total
	}
	if watchedUpTo <= 0 {
		return nil
	}

	mediaType := provider.MediaTypeAnime
	if strings.EqualFold(e.Media.Format, "MOVIE") {
		mediaType = provider.MediaTypeMovie
	}
	watchedAt := time.Now()
	if e.UpdatedAt > 0 {
		watchedAt = time.Unix(e.UpdatedAt, 0)
	}

	out := make([]WatchedItem, 0, watchedUpTo)
	for n := 1; n <= watchedUpTo; n++ {
		out = append(out, WatchedItem{
			Title:     title,
			Mode:      string(provider.ModeAnime),
			MediaType: mediaType,
			Season:    1,
			Episode:   n,
			AniListID: e.Media.ID,
			Complete:  n < watchedUpTo || completed,
			WatchedAt: watchedAt,
		})
	}
	return out
}

// FetchWatchedHistory returns the user's Trakt watch history (episodes and
// movies) as watch items, newest first. History carries per-play
// timestamps, so every item is marked complete with its real watched-at
// time. Repeats collapse downstream in the merge layer.
func (c *TraktClient) FetchWatchedHistory(ctx context.Context) ([]WatchedItem, error) {
	if !c.IsAuthenticated() {
		return nil, fmt.Errorf("trakt not connected")
	}
	// Best effort: a failed refresh must not block the fetch — the
	// access token may still be valid (clock skew, transient outage).
	// A truly dead grant surfaces as a 401 on the history call below.
	if err := c.RefreshIfNeeded(ctx); err != nil {
		traktLog.Debug("trakt refresh failed, trying current token", "err", err)
	}

	const perPage = 250 // API max page size; the server may cap lower
	const maxPages = 100
	var out []WatchedItem
	for page := 1; page <= maxPages; page++ {
		raw, pageCount, pageSize, err := c.fetchHistoryPage(ctx, page, perPage)
		if err != nil {
			return nil, err
		}
		for _, r := range raw {
			if item, ok := mapTraktHistoryItem(r); ok {
				out = append(out, item)
			}
		}
		// The page is exhausted when the API returns fewer raw
		// records than the page actually holds: mapped items can be
		// fewer when records are skipped, which must not stop
		// pagination, and the requested size may be capped below
		// what was asked for.
		if page >= pageCount || len(raw) < pageSize {
			break
		}
	}
	return out, nil
}

type traktHistoryItem struct {
	Type      string `json:"type"`
	WatchedAt string `json:"watched_at"`
	Show      struct {
		Title string `json:"title"`
		IDs   struct {
			TMDB int `json:"tmdb"`
		} `json:"ids"`
	} `json:"show"`
	Episode struct {
		Season int `json:"season"`
		Number int `json:"number"`
	} `json:"episode"`
	Movie struct {
		Title string `json:"title"`
		IDs   struct {
			TMDB int `json:"tmdb"`
		} `json:"ids"`
	} `json:"movie"`
}

// fetchHistoryPage pulls one page of /sync/history and reports the total
// page count plus the page's actual size from the pagination headers
// (defaulting to the request values when absent). Raw records are
// returned unmapped so the caller can page on the raw count; mapping
// skips records and must not affect pagination.
func (c *TraktClient) fetchHistoryPage(ctx context.Context, page, limit int) (raw []traktHistoryItem, pageCount, pageSize int, err error) {
	url := fmt.Sprintf("%s/sync/history?limit=%d&page=%d", c.endpoint(), limit, page)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, 0, 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.token.AccessToken)
	req.Header.Set("trakt-api-version", "2")
	req.Header.Set("trakt-api-key", c.clientID)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, 0, 0, fmt.Errorf("trakt history: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, 0, 0, fmt.Errorf("trakt history: status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, 0, 0, fmt.Errorf("trakt history: decode: %w", err)
	}

	pageCount, pageSize = page, limit
	if v := strings.TrimSpace(resp.Header.Get("X-Pagination-Page-Count")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= page {
			pageCount = n
		}
	}
	if v := strings.TrimSpace(resp.Header.Get("X-Pagination-Limit")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			pageSize = n
		}
	}
	return raw, pageCount, pageSize, nil
}

// mapTraktHistoryItem converts one history record, skipping types and
// shapes kari has no local equivalent for.
func mapTraktHistoryItem(r traktHistoryItem) (WatchedItem, bool) {
	watchedAt, err := time.Parse(time.RFC3339, strings.TrimSpace(r.WatchedAt))
	if err != nil {
		// A missing clock value must not lose the watch itself; the
		// entry lands in "today" rather than nowhere.
		watchedAt = time.Now()
	}
	switch r.Type {
	case "episode":
		title := strings.TrimSpace(r.Show.Title)
		if title == "" || r.Episode.Number <= 0 || r.Episode.Season < 0 {
			return WatchedItem{}, false
		}
		return WatchedItem{
			Title:     title,
			Mode:      string(provider.ModeTV),
			MediaType: provider.MediaTypeTV,
			Season:    r.Episode.Season,
			Episode:   r.Episode.Number,
			TMDBID:    r.Show.IDs.TMDB,
			Complete:  true,
			WatchedAt: watchedAt,
		}, true
	case "movie":
		title := strings.TrimSpace(r.Movie.Title)
		if title == "" {
			return WatchedItem{}, false
		}
		return WatchedItem{
			Title:     title,
			Mode:      string(provider.ModeMovies),
			MediaType: provider.MediaTypeMovie,
			TMDBID:    r.Movie.IDs.TMDB,
			Complete:  true,
			WatchedAt: watchedAt,
		}, true
	default:
		return WatchedItem{}, false
	}
}
