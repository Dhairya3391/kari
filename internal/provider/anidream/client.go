// Package anidream implements provider.Provider, provider.EpisodeSource, and
// provider.MangaSource for AniDream (https://anidream.to).
//
// AniDream is a TanStack Start SPA. Its catalog, playback, and reader data do
// not come from a conventional REST API: the browser calls hashed
// /_serverFn/<hash> endpoints whose arguments travel in a Seroval-encoded
// query parameter. Those server functions are AniList-keyed, so search
// results and episode handles stay compatible with Kari's other anime
// providers while manga chapters use AniDream's own chapter slugs.
//
// Playback returns a signed multi-bitrate HLS master playlist with embedded
// native/English audio and soft ASS/SRT subtitles, plus chapter markers that
// become provider-declared skip times. Manga, novel, and comic entries share
// the single paged-reading capability.
package anidream

import (
	"context"
	"errors"
	"fmt"
	"html"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"

	"kari/internal/config"
	"kari/internal/httpclient"
	"kari/internal/lang"
	"kari/internal/logging"
	"kari/internal/provider"
	"kari/internal/provider/kit"
)

const (
	// providerID is the stable registry identifier.
	providerID = "anidream"
	// searchLimit caps one browse page. The upstream maximum is 50.
	searchLimit = 25
	// playbackServer is the site's default stream lane. The fallback lane
	// covers episodes the primary server has not published yet.
	playbackServer = 2
	fallbackServer = 1
	// chapterCacheTTL bounds how long a successful listing is reused before
	// the source is consulted again. AniDream's chapter endpoint is served
	// from a cold cache on first hits, so repeat listings within a session
	// should not each pay that pause again.
	chapterCacheTTL = 10 * time.Minute
)

// Server-function hashes. TanStack Start derives these from the server
// function's source; they are stable for a deployed bundle.
const (
	fnBrowse       = "106b82f7a2b5783e3a3aed32e330e18e28a5006cded7804c38ebc41464230772"
	fnEpisodes     = "33c4f1e3d3bb451ddccfbb87aeb0d7e52f86a1c8954c61f38d31ea4527593190"
	fnPlayback     = "5ebed94ee2be52c6280fe2b832298a7f8a78c1dc67d7f28b42231743b2ebde1a"
	fnChapters     = "cb787aeb9dcbcf690b192102be9d817a2d6b1923284dccf082b32b6626a06c4a"
	fnChapterPages = "e5496e32e0244b15c11b4820f6167067ad738c6f6359f943ed5100c59a4b9746"
)

// adLog scopes every line from this package with its identity.
var adLog = logging.With("component", "provider.anidream")

// Client implements the anime and manga capabilities of AniDream.
type Client struct {
	http           *http.Client
	baseURL        string
	chaptersMu     sync.Mutex
	chapters       map[int]chapterCacheEntry
	chapterFlights singleflight.Group
}

// chapterCacheEntry is one cached chapter listing keyed by AniList id.
type chapterCacheEntry struct {
	chapters []provider.MangaChapter
	cachedAt time.Time
}

// NewClient constructs the AniDream provider against production.
func NewClient() (*Client, error) {
	return NewClientWithBase(config.AniDreamBase)
}

// NewClientWithBase constructs the AniDream provider against a custom base
// URL. Tests point it at an httptest server.
func NewClientWithBase(baseURL string) (*Client, error) {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		return nil, fmt.Errorf("anidream: empty base URL")
	}
	return &Client{
		http:     httpclient.New(),
		baseURL:  baseURL,
		chapters: map[int]chapterCacheEntry{},
	}, nil
}

// Name implements provider.Provider, returning the stable identifier.
func (c *Client) Name() string { return providerID }

// Alias implements provider.Presenter, providing the display codename.
func (c *Client) Alias() string { return "AniDream" }

// Modes implements provider.Provider, registering anime and the manga/light-
// novel reader.
func (c *Client) Modes() []provider.Mode {
	return []provider.Mode{
		{Name: provider.ModeAnime, Priority: 2},
		{Name: provider.ModeManga, Priority: 2},
	}
}

// Features implements provider.FeatureSource.
func (c *Client) Features(mode provider.ContentType) provider.Features {
	switch mode {
	case provider.ModeAnime:
		return provider.Features{AudioSelection: true}
	case provider.ModeManga:
		return provider.Features{SearchPlaceholder: "Search manga, novels & comics…"}
	default:
		return provider.Features{}
	}
}

// RequiresEpisodeListForMovies implements provider.MovieEpisodeFlow. AniDream
// plays movies through the same AniList-keyed episode list as series.
func (c *Client) RequiresEpisodeListForMovies() bool { return true }

// Search finds anime or manga/light-novel titles through the catalog browse
// server function. AniDream accepts an empty query, but Kari only calls this
// with a real search term.
func (c *Client) Search(ctx context.Context, query string, mode provider.ContentType) ([]provider.SearchResult, error) {
	mediaType, ok := browseType(mode)
	if !ok {
		return nil, fmt.Errorf("anidream search: unsupported mode %q", mode)
	}
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, fmt.Errorf("anidream search: empty query")
	}
	adLog.Debug("search start", "query", query, "mode", mode)

	var response browseResponse
	input := map[string]any{
		"type":    mediaType,
		"q":       query,
		"page":    1,
		"limit":   searchLimit,
		"isAdult": false,
	}
	if err := c.call(ctx, fnBrowse, input, &response); err != nil {
		return nil, fmt.Errorf("anidream search: %w", err)
	}

	results := make([]provider.SearchResult, 0, len(response.Items))
	for _, item := range response.Items {
		if item.ID <= 0 {
			continue
		}
		title := item.Title.label()
		if title == "" {
			continue
		}
		mediaType := mangaMediaType(item.Format)
		result := provider.SearchResult{
			Title:     title,
			ID:        strconv.Itoa(item.ID),
			Type:      mode,
			MediaType: mediaType,
			MALID:     item.MALID,
			CoverURL:  item.CoverImage.Large,
			Overview:  cleanSynopsis(item.Description),
			Genres:    item.Genres,
		}
		if mode == provider.ModeAnime {
			result.MediaType = provider.MediaTypeAnime
			if strings.EqualFold(item.Format, "MOVIE") {
				result.MediaType = provider.MediaTypeMovie
			}
			result.AniListID = item.ID
		}
		if item.StartDate.Year > 0 {
			result.Year = strconv.Itoa(item.StartDate.Year)
		}
		results = append(results, result)
	}
	if len(results) == 0 {
		return nil, provider.ErrNoResults
	}
	adLog.Debug("search done", "query", query, "results", len(results))
	return results, nil
}

// FetchEpisodes lists every episode across both audio tracks the catalog
// advertises. Each row carries the provider handle ResolveSource needs.
func (c *Client) FetchEpisodes(ctx context.Context, series provider.SearchResult) ([]provider.Episode, error) {
	id, err := parseSeriesID(series.ID)
	if err != nil {
		return nil, fmt.Errorf("anidream episodes: %w", err)
	}
	adLog.Debug("fetch episodes start", "anilistID", id)

	var entries []episodeEntry
	if err := c.call(ctx, fnEpisodes, map[string]any{"id": id}, &entries); err != nil {
		return nil, fmt.Errorf("anidream episodes: %w", err)
	}
	if len(entries) == 0 {
		return nil, provider.ErrNoEpisodes
	}

	season := kit.ParseSeason(series.Title)
	if season <= 0 {
		season = 1
	}
	out := make([]provider.Episode, 0, len(entries)*2)
	for _, entry := range entries {
		number := int(entry.Number)
		if number <= 0 || entry.Number != float64(number) {
			continue
		}
		title := entry.Title.label()
		if title == "" {
			title = fmt.Sprintf("Episode %d", number)
		}
		for _, audio := range entry.audioTracks() {
			out = append(out, provider.Episode{
				Title:   title,
				ID:      episodeHandle(id, audio, number),
				Episode: number,
				Season:  season,
				Audio:   audio,
				Filler:  entry.IsFiller,
			})
		}
	}
	if len(out) == 0 {
		return nil, provider.ErrNoEpisodes
	}
	adLog.Debug("fetch episodes done", "anilistID", id, "count", len(out))
	return out, nil
}

// ResolveSource resolves one sub or dub handle to AniDream's signed HLS
// master playlist, mapping chapter markers to skip times and exposing every
// soft subtitle track.
func (c *Client) ResolveSource(ctx context.Context, mediaID string, episode provider.Episode) ([]provider.MediaSource, error) {
	handle := kit.ParseHandle(mediaID, episode.ID, episode.Audio, episode.Episode, providerID)
	if !handle.Valid() {
		return nil, fmt.Errorf("anidream resolve: invalid episode parameters")
	}
	anilistID, err := strconv.Atoi(handle.AniListID)
	if err != nil || anilistID <= 0 {
		return nil, fmt.Errorf("anidream resolve: invalid AniList id %q", handle.AniListID)
	}
	dub := handle.Category == provider.AudioDub
	adLog.Debug("resolve start", "anilistID", anilistID, "episode", handle.Number, "dub", dub)

	var lastErr error
	for _, server := range []int{playbackServer, fallbackServer} {
		input := map[string]any{
			"anilistId": anilistID,
			"episode":   handle.Number,
			"dub":       dub,
			"server":    server,
		}
		var response playbackResponse
		if err := c.call(ctx, fnPlayback, input, &response); err != nil {
			lastErr = err
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			continue
		}
		if !response.OK || response.Data == nil {
			if strings.EqualFold(response.Code, "NOT_FOUND") {
				lastErr = fmt.Errorf("%s: %w", response.Message, provider.ErrNotFound)
			} else {
				lastErr = fmt.Errorf("%s: %w", response.Message, provider.ErrNoSources)
			}
			continue
		}
		data := response.Data
		if !data.supportsAudio(dub) {
			lastErr = fmt.Errorf("no %s audio: %w", handle.Category, provider.ErrAudioUnavailable)
			continue
		}
		if strings.TrimSpace(data.StreamURL) == "" {
			lastErr = fmt.Errorf("empty stream URL: %w", provider.ErrNoSources)
			continue
		}
		source := provider.MediaSource{
			URL:       data.StreamURL,
			Quality:   "Auto",
			Referer:   config.AniDreamReferer,
			Type:      provider.SourceTypeHLS,
			UserAgent: config.DesktopUserAgent,
			SubType:   provider.SubTypeSoft,
			Subtitles: subtitleOptions(data.Subtitles),
			SkipTimes: skipTimes(data.Chapters),
		}
		sources := provider.FilterDirectSources([]provider.MediaSource{source})
		if len(sources) == 0 {
			lastErr = fmt.Errorf("stream is not directly playable: %w", provider.ErrNoSources)
			continue
		}
		adLog.Debug("resolve done", "anilistID", anilistID, "episode", handle.Number, "server", server, "subtitles", len(source.Subtitles))
		return sources, nil
	}
	if dub && (errors.Is(lastErr, provider.ErrNotFound) ||
		errors.Is(lastErr, provider.ErrNoSources) ||
		errors.Is(lastErr, provider.ErrAudioUnavailable)) {
		return nil, fmt.Errorf("anidream resolve: no dub stream: %w", provider.ErrAudioUnavailable)
	}
	if lastErr == nil {
		lastErr = provider.ErrNoSources
	}
	return nil, fmt.Errorf("anidream resolve: %w", lastErr)
}

// FetchChapters lists a manga or light-novel title's chapters in reading
// order. AniDream chapter handles combine the AniList id with the chapter
// slug so FetchPages can address the chapter without extra state. Listings
// are cached per AniList id until the cache TTL lapses.
func (c *Client) FetchChapters(ctx context.Context, series provider.SearchResult) ([]provider.MangaChapter, error) {
	id, err := parseSeriesID(series.ID)
	if err != nil {
		return nil, fmt.Errorf("anidream chapters: %w", err)
	}
	if cached, ok := c.cachedChapters(id); ok {
		return cached, nil
	}
	value, err, _ := c.chapterFlights.Do(strconv.Itoa(id), func() (any, error) {
		if cached, ok := c.cachedChapters(id); ok {
			return cached, nil
		}
		adLog.Debug("fetch chapters start", "anilistID", id)

		var entries []chapterEntry
		if err := c.call(ctx, fnChapters, map[string]any{"id": id}, &entries); err != nil {
			return nil, fmt.Errorf("anidream chapters: %w", err)
		}
		if len(entries) == 0 {
			return nil, provider.ErrNoEpisodes
		}

		out := make([]provider.MangaChapter, 0, len(entries))
		for _, entry := range entries {
			if strings.TrimSpace(entry.Slug) == "" {
				continue
			}
			number := formatChapterNumber(entry.Number)
			out = append(out, provider.MangaChapter{
				ID:       fmt.Sprintf("%d|%s", id, entry.Slug),
				Number:   number,
				Title:    chapterTitle(entry.Name, number),
				Language: "en",
			})
		}
		if len(out) == 0 {
			return nil, provider.ErrNoEpisodes
		}
		sort.SliceStable(out, func(i, j int) bool {
			return chapterOrder(out[i].Number) < chapterOrder(out[j].Number)
		})
		adLog.Debug("fetch chapters done", "anilistID", id, "count", len(out))
		c.storeChapters(id, out)
		return out, nil
	})
	if err != nil {
		return nil, err
	}
	chapters, ok := value.([]provider.MangaChapter)
	if !ok {
		return nil, fmt.Errorf("anidream chapters: unexpected result type %T", value)
	}
	return cloneMangaChapters(chapters), nil
}

// cachedChapters returns the cached chapter listing for an AniList id when
// it is still inside the cache TTL. Failed listings are never cached, so a
// transient upstream error is retried on the next call.
func (c *Client) cachedChapters(id int) ([]provider.MangaChapter, bool) {
	c.chaptersMu.Lock()
	defer c.chaptersMu.Unlock()
	entry, ok := c.chapters[id]
	if !ok || time.Since(entry.cachedAt) > chapterCacheTTL {
		delete(c.chapters, id)
		return nil, false
	}
	return cloneMangaChapters(entry.chapters), true
}

// storeChapters remembers a successful chapter listing until the cache TTL
// lapses.
func (c *Client) storeChapters(id int, chapters []provider.MangaChapter) {
	c.chaptersMu.Lock()
	defer c.chaptersMu.Unlock()
	c.chapters[id] = chapterCacheEntry{chapters: cloneMangaChapters(chapters), cachedAt: time.Now()}
}

func cloneMangaChapters(chapters []provider.MangaChapter) []provider.MangaChapter {
	return append([]provider.MangaChapter(nil), chapters...)
}

func mangaMediaType(format string) string {
	switch strings.ToUpper(strings.TrimSpace(format)) {
	case "NOVEL", "LIGHT_NOVEL", "LIGHT NOVEL":
		return provider.MediaTypeNovel
	case "COMIC", "GRAPHIC_NOVEL", "GRAPHIC NOVEL":
		return provider.MediaTypeComic
	default:
		return provider.MediaTypeManga
	}
}

// FetchPages resolves one chapter's page-image URLs.
func (c *Client) FetchPages(ctx context.Context, chapter provider.MangaChapter) ([]provider.MangaPage, error) {
	seriesID, slug, ok := strings.Cut(chapter.ID, "|")
	if !ok {
		return nil, fmt.Errorf("anidream pages: malformed chapter handle %q", chapter.ID)
	}
	id, err := parseSeriesID(seriesID)
	if err != nil {
		return nil, fmt.Errorf("anidream pages: %w", err)
	}

	var entries []pageEntry
	input := map[string]any{"id": id, "chapter": slug}
	if err := c.call(ctx, fnChapterPages, input, &entries); err != nil {
		return nil, fmt.Errorf("anidream pages: %w", err)
	}
	if len(entries) == 0 {
		return nil, provider.ErrNoSources
	}

	sort.SliceStable(entries, func(i, j int) bool { return entries[i].Index < entries[j].Index })
	pages := make([]provider.MangaPage, 0, len(entries))
	seen := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		rawURL := strings.TrimSpace(entry.URL)
		if rawURL == "" {
			continue
		}
		if _, dup := seen[rawURL]; dup {
			continue
		}
		seen[rawURL] = struct{}{}
		pages = append(pages, provider.MangaPage{
			URL:       rawURL,
			UserAgent: config.DesktopUserAgent,
		})
	}
	if len(pages) == 0 {
		return nil, provider.ErrNoSources
	}
	adLog.Debug("fetch pages done", "anilistID", id, "pages", len(pages))
	return pages, nil
}

// call invokes one TanStack server function and decodes its handler result
// into out. The payload is a Seroval-wrapped {"data": input} object.
func (c *Client) call(ctx context.Context, hash string, input map[string]any, out any) error {
	payload, err := marshalServerFnPayload(map[string]any{"data": input})
	if err != nil {
		return fmt.Errorf("encode payload: %w", err)
	}
	endpoint := c.baseURL + "/_serverFn/" + hash + "?payload=" + url.QueryEscape(string(payload))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-TSR-ServerFn", "true")
	req.Header.Set("Referer", config.AniDreamReferer)
	req.Header.Set("Origin", c.baseURL)
	req.Header.Set("User-Agent", config.DesktopUserAgent)

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("fetch server function: %w", err)
	}
	defer resp.Body.Close()
	body, err := httpclient.ReadCapped(resp)
	if err != nil {
		return fmt.Errorf("read server function: %w", err)
	}
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound:
		return fmt.Errorf("server function %s: %w", hash, provider.ErrNotFound)
	case http.StatusForbidden:
		return fmt.Errorf("server function %s: %w", hash, provider.ErrBlocked)
	case http.StatusTooManyRequests:
		return fmt.Errorf("server function %s: %w", hash, provider.ErrRateLimited)
	default:
		return &provider.HTTPError{Code: resp.StatusCode, URL: c.baseURL + "/_serverFn/" + hash}
	}

	result, err := decodeServerFnResponse(body)
	if err != nil {
		return err
	}
	return decodeResult(result, out)
}

// browseType maps a Kari mode to AniDream's catalog type vocabulary.
func browseType(mode provider.ContentType) (string, bool) {
	switch mode {
	case provider.ModeAnime:
		return "ANIME", true
	case provider.ModeManga:
		return "MANGA", true
	default:
		return "", false
	}
}

// parseSeriesID validates an AniList-keyed provider handle.
func parseSeriesID(raw string) (int, error) {
	id, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || id <= 0 {
		return 0, fmt.Errorf("invalid AniList id %q", raw)
	}
	return id, nil
}

// episodeHandle encodes everything ResolveSource needs into provider.Episode.ID.
func episodeHandle(anilistID int, audio string, number int) string {
	return fmt.Sprintf("watch/%s/%d/%s/%d", providerID, anilistID, audio, number)
}

// formatChapterNumber renders chapter numbers as the catalog's decimals
// (8.1, 12.5) without trailing zeros.
func formatChapterNumber(number float64) string {
	if number <= 0 {
		return ""
	}
	return strconv.FormatFloat(number, 'f', -1, 64)
}

// chapterTitle drops the catalog's redundant "Chapter N" prefix so the
// reader label does not read "Ch 5 — Chapter 5". The remainder (a real
// subtitle) is kept.
func chapterTitle(name, number string) string {
	name = strings.TrimSpace(name)
	if name == "" || number == "" {
		return name
	}
	prefix := "chapter " + strings.ToLower(number)
	lower := strings.ToLower(name)
	if lower == prefix {
		return ""
	}
	if strings.HasPrefix(lower, prefix) {
		rest := strings.TrimSpace(name[len(prefix):])
		return strings.TrimLeft(rest, ":-–— ")
	}
	return name
}

// chapterOrder parses a display chapter number for sorting; unnumbered rows
// sort last.
func chapterOrder(number string) float64 {
	value, err := strconv.ParseFloat(strings.TrimSpace(number), 64)
	if err != nil || value <= 0 {
		return 1e18
	}
	return value
}

// subtitleOptions maps AniDream's subtitle tracks to the shared DTO,
// normalizing language names to short codes.
func subtitleOptions(tracks []playbackSubtitle) []provider.SubtitleOption {
	out := make([]provider.SubtitleOption, 0, len(tracks))
	seen := make(map[string]struct{}, len(tracks))
	for _, track := range tracks {
		rawURL := strings.TrimSpace(track.URL)
		if rawURL == "" {
			continue
		}
		key := rawURL + "\x00" + track.Language
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, provider.SubtitleOption{
			URL:      rawURL,
			Language: lang.Normalize(track.Language),
			Default:  track.Default,
			Referer:  config.AniDreamReferer,
		})
	}
	return out
}

// skipTimes converts AniDream's chapter markers into provider skip bounds.
func skipTimes(chapters []playbackChapter) *provider.SkipTimes {
	if len(chapters) == 0 {
		return nil
	}
	bounds := &provider.SkipTimes{
		Source:       providerID,
		OpStart:      -1,
		OpEnd:        -1,
		EdStart:      -1,
		EdEnd:        -1,
		RecapStart:   -1,
		RecapEnd:     -1,
		PreviewStart: -1,
		PreviewEnd:   -1,
	}
	for _, chapter := range chapters {
		if chapter.End <= chapter.Start {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(chapter.Type)) {
		case "intro":
			bounds.OpStart, bounds.OpEnd = chapter.Start, chapter.End
		case "outro":
			bounds.EdStart, bounds.EdEnd = chapter.Start, chapter.End
		case "recap":
			bounds.RecapStart, bounds.RecapEnd = chapter.Start, chapter.End
		case "preview":
			bounds.PreviewStart, bounds.PreviewEnd = chapter.Start, chapter.End
		}
	}
	if !bounds.HasSegments() {
		return nil
	}
	return bounds
}

// Synopsis text arrives from AniList with escaped HTML entities. Convert
// them back to markup, turn breaks into newlines, and strip the remaining
// tags so detail screens show readable prose.
var (
	brTagRe        = regexp.MustCompile(`(?i)<br\s*/?>`)
	htmlTagRe      = regexp.MustCompile(`<[^>]*>`)
	extraNewlineRe = regexp.MustCompile(`\n{3,}`)
	aniListEscapes = strings.NewReplacer(
		`\x3C`, "<",
		`\x3E`, ">",
		`\x26`, "&",
		`\x22`, `"`,
		`\x27`, "'",
		`\x3D`, "=",
		`\n`, "\n",
		`\r`, "",
	)
)

// cleanSynopsis normalizes AniList's escaped markup into plain text.
func cleanSynopsis(raw string) string {
	if strings.TrimSpace(raw) == "" {
		return ""
	}
	synopsis := aniListEscapes.Replace(raw)
	synopsis = brTagRe.ReplaceAllString(synopsis, "\n")
	synopsis = htmlTagRe.ReplaceAllString(synopsis, "")
	synopsis = html.UnescapeString(synopsis)
	synopsis = strings.TrimSpace(extraNewlineRe.ReplaceAllString(synopsis, "\n\n"))
	return synopsis
}

// mediaTitle is AniList's three-language title object.
type mediaTitle struct {
	Romaji  string `json:"romaji"`
	English string `json:"english"`
	Native  string `json:"native"`
}

// label prefers English, then romaji, then the native title.
func (t mediaTitle) label() string {
	for _, candidate := range []string{t.English, t.Romaji, t.Native} {
		if title := strings.TrimSpace(candidate); title != "" {
			return title
		}
	}
	return ""
}

// coverImage is AniList's cover-size object.
type coverImage struct {
	Large  string `json:"large"`
	Medium string `json:"medium"`
	Small  string `json:"small"`
}

// partialDate is AniList's start_date object.
type partialDate struct {
	Year int `json:"year"`
}

// browseItem is one catalog card returned by the browse server function.
type browseItem struct {
	ID          int         `json:"id"`
	MALID       int         `json:"mal_id"`
	Format      string      `json:"format"`
	Title       mediaTitle  `json:"title"`
	StartDate   partialDate `json:"start_date"`
	CoverImage  coverImage  `json:"cover_image"`
	Description string      `json:"description"`
	Genres      []string    `json:"genres"`
}

// browseResponse is the paginated browse/search result.
type browseResponse struct {
	Items []browseItem `json:"items"`
}

// episodeEntry is one episode-list row.
type episodeEntry struct {
	Number   float64    `json:"number"`
	Title    mediaTitle `json:"title"`
	HasSub   bool       `json:"has_sub"`
	HasDub   bool       `json:"has_dub"`
	IsFiller bool       `json:"is_filler"`
	IsRecap  bool       `json:"is_recap"`
}

// audioTracks returns the audio variants the catalog declares. Entries with
// no flags fall back to sub so the episode stays addressable.
func (e episodeEntry) audioTracks() []string {
	var tracks []string
	if e.HasSub {
		tracks = append(tracks, provider.AudioSub)
	}
	if e.HasDub {
		tracks = append(tracks, provider.AudioDub)
	}
	if len(tracks) == 0 {
		tracks = append(tracks, provider.AudioSub)
	}
	return tracks
}

// playbackResponse is the playback server function's result. Failures use
// the same envelope with OK=false and a message.
type playbackResponse struct {
	OK      bool          `json:"ok"`
	Code    string        `json:"code"`
	Message string        `json:"message"`
	Data    *playbackData `json:"data"`
}

// playbackData is one resolved stream.
type playbackData struct {
	FileCode  string             `json:"fileCode"`
	StreamURL string             `json:"streamUrl"`
	Subtitles []playbackSubtitle `json:"subtitles"`
	Chapters  []playbackChapter  `json:"chapters"`
	Audio     string             `json:"audio"`
	Audios    []string           `json:"audios"`
	Server    int                `json:"server"`
	ExpiresAt string             `json:"expiresAt"`
}

// supportsAudio reports whether the resolved stream carries the requested
// audio variant. An empty list is treated as permissive because the master
// playlist still lets the player select tracks.
func (d *playbackData) supportsAudio(dub bool) bool {
	if len(d.Audios) == 0 {
		return true
	}
	want := provider.AudioSub
	if dub {
		want = provider.AudioDub
	}
	for _, audio := range d.Audios {
		normalized := strings.ToLower(strings.TrimSpace(audio))
		if normalized == want || normalized == "dual" || normalized == "multi" {
			return true
		}
	}
	return false
}

// playbackSubtitle is one soft subtitle track.
type playbackSubtitle struct {
	Label    string `json:"label"`
	URL      string `json:"url"`
	Type     string `json:"type"`
	Language string `json:"language"`
	Default  bool   `json:"default"`
}

// playbackChapter is one chapter marker; Type is set for intro/outro.
type playbackChapter struct {
	Title string  `json:"title"`
	Start float64 `json:"start"`
	End   float64 `json:"end"`
	Type  string  `json:"type"`
}

// chapterEntry is one manga/light-novel chapter row.
type chapterEntry struct {
	Slug   string  `json:"slug"`
	Number float64 `json:"number"`
	Name   string  `json:"name"`
}

// pageEntry is one page image row.
type pageEntry struct {
	URL   string `json:"url"`
	Index int    `json:"index"`
}

var (
	_ provider.Provider         = (*Client)(nil)
	_ provider.EpisodeSource    = (*Client)(nil)
	_ provider.MangaSource      = (*Client)(nil)
	_ provider.FeatureSource    = (*Client)(nil)
	_ provider.MovieEpisodeFlow = (*Client)(nil)
	_ provider.Presenter        = (*Client)(nil)
)
