// Package vidrift implements provider.Provider for VidRift
// (https://embed.vidrift.net), an embed player keyed by TMDB and AniList
// IDs.
//
// VidRift renders all playable lanes into its embed page, then resolves
// individual lanes through /api/source. This provider reads the embed
// metadata for the playback token, asks the same API for every production
// lane in parallel, and returns the resulting HLS addresses directly —
// mpv plays them with VidRift's Referer, no page or iframe is involved.
package vidrift

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"kari/internal/config"
	"kari/internal/httpclient"
	"kari/internal/lang"
	"kari/internal/logging"
	"kari/internal/provider"
	"kari/internal/provider/anilist"
	"kari/internal/provider/kit"
	"kari/internal/provider/streambase"
	"kari/internal/tmdb"
	"kari/internal/util"
)

const (
	// providerID is the stable registry identifier.
	providerID = "vidrift"
	// providerAnime is VidRift's subbed anime lane.
	providerAnime = "anime"
	// providerAnimeDub is VidRift's English-dubbed anime lane.
	providerAnimeDub = "animedub"

	// laneBudget caps how long a resolve waits for the slowest lane. Lanes
	// still running when it expires are discarded so one stuck scraper
	// cannot hold the whole provider to the service timeout.
	defaultLaneBudget = 10 * time.Second
	// embedCacheTTL and laneCacheTTL keep re-resolves (audio toggle, retry,
	// returning to an episode) from repeating upstream work. Both stay far
	// below the signed-URL lifetimes VidRift issues.
	embedCacheTTL = 5 * time.Minute
	laneCacheTTL  = 2 * time.Minute
	// cache sizes bound memory: each entry holds parsed metadata or one
	// lane's URLs, not response bodies.
	embedCacheSize = 32
	laneCacheSize  = 128
)

// standardSourceProviders are the general movie/TV lanes VidRift's player
// tries, in its own cascade order. Anime is resolved only through its own
// lanes: generic scrapers cannot prove Japanese audio, and the sub/dub
// selector already owns that choice.
var standardSourceProviders = []string{"orion", "vela", "atlas", "titan", "lyra"}

// embedCacheEntry is one parsed embed document and when it was fetched.
type embedCacheEntry struct {
	page      embedPage
	fetchedAt time.Time
}

// laneCacheEntry is one resolved lane and when it was fetched.
type laneCacheEntry struct {
	result    laneResult
	fetchedAt time.Time
}

// vidriftAudioLanguages is the full set of audio-track languages VidRift
// lanes can emit. It backs AudioLanguagesSource so settings can offer
// per-language filters, and doubles as the whitelist for source tagging:
// labels that do not normalize onto this list stay untagged instead of
// sending a bogus --alang value to mpv.
var vidriftAudioLanguages = []provider.AudioLanguage{
	{Code: "en", Display: "English"},
	{Code: "hi", Display: "Hindi"},
	{Code: "ta", Display: "Tamil"},
	{Code: "te", Display: "Telugu"},
	{Code: "ml", Display: "Malayalam"},
	{Code: "kn", Display: "Kannada"},
	{Code: "bn", Display: "Bengali"},
	{Code: "mr", Display: "Marathi"},
	{Code: "gu", Display: "Gujarati"},
	{Code: "pa", Display: "Punjabi"},
	{Code: "ur", Display: "Urdu"},
	{Code: "ar", Display: "Arabic"},
	{Code: "es", Display: "Spanish"},
	{Code: "fr", Display: "French"},
	{Code: "de", Display: "German"},
	{Code: "it", Display: "Italian"},
	{Code: "pt", Display: "Portuguese"},
	{Code: "ru", Display: "Russian"},
	{Code: "ja", Display: "Japanese"},
	{Code: "ko", Display: "Korean"},
	{Code: "zh", Display: "Chinese"},
	{Code: "tr", Display: "Turkish"},
	{Code: "id", Display: "Indonesian"},
	{Code: "th", Display: "Thai"},
	{Code: "vi", Display: "Vietnamese"},
	{Code: "ms", Display: "Malay"},
	{Code: "uk", Display: "Ukrainian"},
	{Code: "pl", Display: "Polish"},
	{Code: "nl", Display: "Dutch"},
	{Code: "sv", Display: "Swedish"},
	{Code: "da", Display: "Danish"},
	{Code: "no", Display: "Norwegian"},
	{Code: "fi", Display: "Finnish"},
	{Code: "cs", Display: "Czech"},
	{Code: "el", Display: "Greek"},
	{Code: "he", Display: "Hebrew"},
	{Code: "fa", Display: "Persian"},
	{Code: "ro", Display: "Romanian"},
	{Code: "hu", Display: "Hungarian"},
	{Code: "bg", Display: "Bulgarian"},
	{Code: "hr", Display: "Croatian"},
	{Code: "sr", Display: "Serbian"},
	{Code: "fil", Display: "Filipino"},
	{Code: "sw", Display: "Swahili"},
}

// Client implements provider.Provider for movies, TV, and anime through
// VidRift's embed API.
type Client struct {
	base        *streambase.Base
	httpClient  *http.Client
	embedBase   string
	anilistBase string
	// langFilter mirrors the user's audio-language settings (see
	// internal/settings LanguageFilter): nil or missing keys mean enabled.
	langFilter map[string]bool
	embedCache *util.BoundedCache[embedCacheEntry]
	laneCache  *util.BoundedCache[laneCacheEntry]
	// laneBudget bounds one lane fan-out; a field so tests can shorten it.
	laneBudget time.Duration
}

// NewClient constructs the VidRift provider over the shared TMDB search
// base and the production embed origin.
func NewClient(keyPool *tmdb.KeyPool) (*Client, error) {
	return NewClientWithLanguageFilter(keyPool, nil)
}

// NewClientWithLanguageFilter is NewClient with the user's audio-language
// filter applied at resolve time (nil = everything enabled).
func NewClientWithLanguageFilter(keyPool *tmdb.KeyPool, langFilter map[string]bool) (*Client, error) {
	return NewClientWithURLsAndLanguageFilter(keyPool, config.VidRiftEmbedBase, config.AniListAPIBase, langFilter)
}

// NewClientWithURLs constructs the VidRift provider against explicit embed
// and AniList endpoints so offline tests can serve canned payloads.
func NewClientWithURLs(keyPool *tmdb.KeyPool, embedBase, anilistBase string) (*Client, error) {
	return NewClientWithURLsAndLanguageFilter(keyPool, embedBase, anilistBase, nil)
}

// NewClientWithURLsAndLanguageFilter is NewClientWithURLs with an explicit
// audio-language filter.
func NewClientWithURLsAndLanguageFilter(keyPool *tmdb.KeyPool, embedBase, anilistBase string, langFilter map[string]bool) (*Client, error) {
	base, err := streambase.New(keyPool)
	if err != nil {
		return nil, fmt.Errorf("vidrift streambase: %w", err)
	}
	embedBase = strings.TrimRight(strings.TrimSpace(embedBase), "/")
	anilistBase = strings.TrimRight(strings.TrimSpace(anilistBase), "/")
	if embedBase == "" || anilistBase == "" {
		return nil, fmt.Errorf("vidrift: embed and AniList endpoints are required")
	}
	return &Client{
		base:        base,
		httpClient:  httpclient.New(),
		embedBase:   embedBase,
		anilistBase: anilistBase,
		langFilter:  langFilter,
		embedCache:  util.NewBoundedCache[embedCacheEntry](embedCacheSize),
		laneCache:   util.NewBoundedCache[laneCacheEntry](laneCacheSize),
		laneBudget:  defaultLaneBudget,
	}, nil
}

// Alias implements provider.Presenter with the user-facing display name.
func (c *Client) Alias() string { return "VidRift" }

// Name implements provider.Provider with the canonical provider identifier.
func (c *Client) Name() string { return providerID }

// Modes implements provider.Provider, declaring movies, TV, and anime.
func (c *Client) Modes() []provider.Mode {
	return []provider.Mode{
		{Name: provider.ModeMovies, Priority: 2},
		{Name: provider.ModeTV, Priority: 2},
		{Name: provider.ModeAnime, Priority: 2},
	}
}

// Features implements provider.FeatureSource. Anime exposes separate sub and
// dub lanes, so episode-level audio selection applies.
func (c *Client) Features(mode provider.ContentType) provider.Features {
	if mode != provider.ModeAnime {
		return provider.Features{}
	}
	return provider.Features{AudioSelection: true}
}

// AudioLanguages implements provider.AudioLanguagesSource.
func (c *Client) AudioLanguages() []provider.AudioLanguage {
	return vidriftAudioLanguages
}

// Search resolves movies and TV through the shared TMDB index and anime
// through the AniList index, matching the identifiers VidRift embeds.
func (c *Client) Search(ctx context.Context, query string, mode provider.ContentType) ([]provider.SearchResult, error) {
	if mode == provider.ModeAnime {
		results, err := anilist.SearchWithEndpoint(ctx, c.httpClient, query, c.anilistBase)
		if err != nil {
			return nil, fmt.Errorf("vidrift anime search: %w", err)
		}
		if len(results) == 0 {
			return nil, provider.ErrNoResults
		}
		return results, nil
	}
	return c.base.Search(ctx, query, mode)
}

// FetchEpisodes lists anime episodes from AniList and delegates movies and
// TV to the shared TMDB catalog.
func (c *Client) FetchEpisodes(ctx context.Context, series provider.SearchResult) ([]provider.Episode, error) {
	if series.AniListID == 0 && series.Type != provider.ModeAnime {
		return c.base.FetchEpisodes(ctx, series)
	}

	anilistID, err := strconv.Atoi(strings.TrimSpace(series.ID))
	if err != nil || anilistID <= 0 {
		return nil, fmt.Errorf("vidrift episodes: invalid AniList id %q: %w", series.ID, provider.ErrNoEpisodes)
	}

	if series.MediaType == provider.MediaTypeMovie {
		return movieEpisodes(series), nil
	}

	media, err := anilist.FetchMediaWithEndpoint(ctx, c.httpClient, anilistID, c.anilistBase)
	if err != nil {
		return nil, fmt.Errorf("vidrift episodes: %w", err)
	}
	count := media.EpisodeCount()
	if count <= 0 {
		return nil, provider.ErrNoEpisodes
	}

	season := kit.ParseSeason(series.Title)
	if season <= 0 {
		season = 1
	}
	episodes := make([]provider.Episode, 0, count*2)
	for number := 1; number <= count; number++ {
		episodes = append(episodes, animeEpisode(series, number, provider.AudioSub, season))
		episodes = append(episodes, animeEpisode(series, number, provider.AudioDub, season))
	}
	logging.Debug("fetch episodes done", "provider", c.Name(), "count", len(episodes))
	return episodes, nil
}

// ResolveSource maps one episode coordinate to an embed route, reads its
// metadata, and resolves the lanes VidRift exposes for that route: every
// general lane for movies and TV, only the requested anime lane for anime.
//
// Anime requests are strict about audio: subbed resolves use only the
// subbed lane, dub resolves only the English dub lane, and a response that
// fell back to another provider is rejected — the fallback's audio track is
// whatever the winner happened to carry. A dub that has no real dub lane is
// reported as ErrAudioUnavailable so the TUI can hide the track instead of
// silently playing the wrong audio.
func (c *Client) ResolveSource(ctx context.Context, mediaID string, episode provider.Episode) ([]provider.MediaSource, error) {
	logging.Debug("resolve source", "provider", c.Name(), "mediaID", mediaID, "episode", episode.Episode, "audio", episode.Audio)
	route, err := routeFor(mediaID, episode)
	if err != nil {
		return nil, err
	}

	page, err := c.fetchEmbed(ctx, route)
	if err != nil {
		return nil, err
	}
	if page.Meta.TMDBID == "" || page.Meta.PlaybackToken == "" {
		return nil, fmt.Errorf("vidrift: incomplete playback metadata: %w", provider.ErrNoSources)
	}

	anime := strings.HasPrefix(route, "anilist/")
	dubbed := strings.EqualFold(strings.TrimSpace(episode.Audio), provider.AudioDub)
	lanes := standardSourceProviders
	if anime {
		// Anime stays on VidRift's own lane. General scrapers do not
		// declare Japanese/English audio, so mixing them would turn the
		// sub/dub choice into a guess and add their cascade latency.
		lane := providerAnime
		if dubbed {
			lane = providerAnimeDub
		}
		// The embed always lists both anime lanes even when a title has no
		// dub, so the lane lookup itself has to prove availability.
		if !page.Meta.hasAnimeLane(lane) {
			if dubbed {
				return nil, fmt.Errorf("vidrift: English dub unavailable: %w", provider.ErrAudioUnavailable)
			}
			return nil, fmt.Errorf("vidrift: subtitled anime unavailable: %w", provider.ErrNoSources)
		}
		lanes = []string{lane}
	}

	laneCtx, cancel := context.WithTimeout(ctx, c.laneBudget)
	defer cancel()
	results := c.fetchLanes(laneCtx, page.Meta.sourcePath(), page.Meta.PlaybackToken, lanes)
	if anime {
		results = filterAnimeLanes(results, dubbed)
	}

	sources := buildSources(page, results, c.embedBase)
	if !anime {
		// Baked BigBang/Evion/Vexon/selfhost lanes carry no reliable audio
		// language, so they are only safe for movies and TV.
		sources = append(sources, bakedSources(page.Meta, c.embedBase)...)
	}
	sources = dedupeSources(sources)
	sources = provider.FilterDirectSources(sources)
	sources = c.filterSourcesByLanguage(sources, anime, dubbed)
	if len(sources) == 0 {
		// Surface transport failures instead of disguising them as an
		// unavailable audio track.
		if laneErr := firstLaneError(results); laneErr != nil {
			return nil, fmt.Errorf("vidrift: %w", laneErr)
		}
		if dubbed {
			return nil, fmt.Errorf("vidrift: English dub unavailable: %w", provider.ErrAudioUnavailable)
		}
		return nil, provider.ErrNoSources
	}

	logging.Debug("resolve source done", "provider", c.Name(), "count", len(sources))
	return sources, nil
}

// fetchEmbed loads one embed page and decodes the metadata the player
// renders server-side. Parsed pages are cached briefly: the playback token
// and lane metadata stay valid for hours, and re-resolves hit the same
// route.
func (c *Client) fetchEmbed(ctx context.Context, route string) (embedPage, error) {
	if entry, ok := c.embedCache.Get(route); ok && time.Since(entry.fetchedAt) < embedCacheTTL {
		return entry.page, nil
	}

	endpoint := c.embedBase + "/embed/" + route
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return embedPage{}, fmt.Errorf("vidrift embed request: %w", err)
	}
	req.Header.Set("User-Agent", config.DesktopUserAgent)
	req.Header.Set("Referer", config.VidRiftSiteBase+"/")
	req.Header.Set("Accept", "text/html,application/xhtml+xml")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return embedPage{}, fmt.Errorf("vidrift embed: %w", err)
	}
	if resp.StatusCode == http.StatusNotFound {
		_ = resp.Body.Close()
		return embedPage{}, fmt.Errorf("vidrift embed: %w", provider.ErrNotFound)
	}
	if resp.StatusCode != http.StatusOK {
		_ = resp.Body.Close()
		return embedPage{}, &provider.HTTPError{Code: resp.StatusCode, URL: endpoint}
	}

	body, err := httpclient.ReadCapped(resp)
	if err != nil {
		return embedPage{}, fmt.Errorf("vidrift embed: %w", err)
	}
	page, err := parseEmbedPage(body)
	if err != nil {
		return embedPage{}, fmt.Errorf("vidrift embed: %w", err)
	}
	c.embedCache.Set(route, embedCacheEntry{page: page, fetchedAt: time.Now()})
	return page, nil
}

// fetchLanes queries every lane concurrently. Results keep the lane order so
// output ordering stays stable even though responses race. Per-lane errors
// are retained rather than aborting the group, so a lane that overruns the
// shared budget does not discard the lanes that already answered.
func (c *Client) fetchLanes(ctx context.Context, route, token string, lanes []string) []laneResult {
	results := make([]laneResult, len(lanes))
	var wg sync.WaitGroup
	for i, lane := range lanes {
		i, lane := i, lane
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, err := c.fetchLane(ctx, route, token, lane)
			if err != nil {
				logging.Debug("vidrift lane lookup failed", "lane", lane, "err", err)
				results[i] = laneResult{lane: lane, err: err}
				return
			}
			results[i] = result
		}()
	}
	wg.Wait()
	return results
}

// fetchLane resolves one source lane through VidRift's /api/source route.
// Successful payloads are cached briefly so audio toggles and retries reuse
// the same signed URLs instead of re-scraping an upstream lane.
func (c *Client) fetchLane(ctx context.Context, route, token, lane string) (laneResult, error) {
	cacheKey := route + "\x00" + token + "\x00" + lane
	if entry, ok := c.laneCache.Get(cacheKey); ok && time.Since(entry.fetchedAt) < laneCacheTTL {
		return entry.result, nil
	}

	params := url.Values{}
	params.Set("token", token)
	params.Set("provider", lane)

	endpoint := c.embedBase + "/api/source/" + route + "?" + params.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return laneResult{}, fmt.Errorf("vidrift source request: %w", err)
	}
	req.Header.Set("User-Agent", config.DesktopUserAgent)
	req.Header.Set("Referer", c.embedBase+"/embed/"+route)
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return laneResult{}, fmt.Errorf("vidrift source: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		_ = resp.Body.Close()
		return laneResult{}, &provider.HTTPError{Code: resp.StatusCode, URL: endpoint}
	}

	body, err := httpclient.ReadCapped(resp)
	if err != nil {
		return laneResult{}, fmt.Errorf("vidrift source: %w", err)
	}
	var payload sourceResponse
	if err := json.Unmarshal(body, &payload); err != nil {
		return laneResult{}, fmt.Errorf("vidrift source: decode response: %w", err)
	}
	if !payload.Success {
		return laneResult{lane: lane}, nil
	}
	result := laneResult{
		lane:      lane,
		streams:   payload.Streams,
		subtitles: payload.Subtitles,
		maxRes:    payload.MaxRes,
		provider:  payload.Provider,
		source:    payload.Source,
		fellBack:  payload.FellBack,
	}
	c.laneCache.Set(cacheKey, laneCacheEntry{result: result, fetchedAt: time.Now()})
	return result, nil
}

// filterAnimeLanes keeps only responses that came from the requested audio
// lane. VidRift's cascade happily falls back to another provider (including
// subbed streams for a dub request), so the response's own source/provider
// fields are the only reliable signal once a lookup succeeds.
func filterAnimeLanes(results []laneResult, dubbed bool) []laneResult {
	filtered := make([]laneResult, 0, len(results))
	for _, result := range results {
		if result.err != nil {
			filtered = append(filtered, result)
			continue
		}
		// A fallback means the requested lane did not answer; its audio
		// track is whatever the winner happened to carry.
		if result.fellBack {
			continue
		}
		if dubbed {
			// A dub request must not accept subbed rows.
			if !result.isDub() {
				continue
			}
		} else if result.isDub() {
			// A sub request must not accept dubbed rows.
			continue
		}
		filtered = append(filtered, result)
	}
	return filtered
}

// firstLaneError returns the first lane failure, or nil when every lane
// answered. ResolveSource consults it only after filtering left no sources:
// partial success normally wins, but a timeout or transport error must be
// surfaced rather than reported as a missing audio track.
func firstLaneError(results []laneResult) error {
	for _, result := range results {
		if result.err != nil {
			return result.err
		}
	}
	return nil
}

// filterSourcesByLanguage applies the user's audio-language settings.
// Anime is exempt: the sub/dub selector already owns that decision, so the
// selected track is kept even if its language was globally disabled, while
// foreign tracks never leak in. For movies and TV, unknown languages pass
// because they cannot be compared to a setting, and a title whose languages
// are all disabled keeps its rows so it never vanishes from the resolve
// screen — playback filtering still honors the setting.
func (c *Client) filterSourcesByLanguage(sources []provider.MediaSource, anime, dubbed bool) []provider.MediaSource {
	if anime {
		want := "ja"
		if dubbed {
			want = "en"
		}
		kept := make([]provider.MediaSource, 0, len(sources))
		for _, source := range sources {
			if code := strings.TrimSpace(source.Language); code == "" || code == want {
				kept = append(kept, source)
			}
		}
		return kept
	}

	kept := make([]provider.MediaSource, 0, len(sources))
	var dropped []provider.MediaSource
	for _, source := range sources {
		if strings.TrimSpace(source.Language) == "" || c.languageEnabled(source.Language) {
			kept = append(kept, source)
			continue
		}
		dropped = append(dropped, source)
	}
	if len(kept) == 0 {
		return dropped
	}
	return kept
}

// languageEnabled mirrors the TUI toggle semantics: a nil filter or a
// missing key means enabled; only an explicit false disables.
func (c *Client) languageEnabled(code string) bool {
	if c.langFilter == nil {
		return true
	}
	display := lang.Name(code)
	for _, key := range []string{code, display} {
		if key == "" {
			continue
		}
		if v, ok := c.langFilter[key]; ok {
			return v
		}
		if v, ok := c.langFilter[strings.ToLower(key)]; ok {
			return v
		}
	}
	return true
}

// movieEpisodes returns the single synthetic anime-movie episode for both
// audio tracks. The AniList embed route resolves movies too.
func movieEpisodes(series provider.SearchResult) []provider.Episode {
	return []provider.Episode{
		{
			Title:   series.Title,
			ID:      episodeID(series.ID, provider.AudioSub, 1),
			Season:  1,
			Episode: 1,
			Audio:   provider.AudioSub,
			TMDBID:  series.TMDBID,
		},
		{
			Title:   series.Title,
			ID:      episodeID(series.ID, provider.AudioDub, 1),
			Season:  1,
			Episode: 1,
			Audio:   provider.AudioDub,
			TMDBID:  series.TMDBID,
		},
	}
}

// animeEpisode builds one numbered anime episode for a sub or dub roster.
func animeEpisode(series provider.SearchResult, number int, audio string, season int) provider.Episode {
	return provider.Episode{
		Title:   fmt.Sprintf("Episode %02d", number),
		ID:      episodeID(series.ID, audio, number),
		Season:  season,
		Episode: number,
		Audio:   audio,
	}
}

// episodeID builds a per-track handle in the same shape the other anime
// providers use. ResolveSource uses the handle (and the audio tag) to tell
// an AniList resolve apart from a TMDB one.
func episodeID(seriesID, audio string, number int) string {
	return fmt.Sprintf("watch/%s/%s/%s/%d", providerID, seriesID, audio, number)
}

// routeFor chooses the embed route family from the handle and episode
// coordinates. TMDB ids and AniList ids are both numeric and MediaService
// synthesizes episode.TMDBID from series.ID for anime, so a matching TMDBID
// alone is ambiguous; an audio tag or a per-episode watch handle marks the
// request as anime.
func routeFor(mediaID string, episode provider.Episode) (string, error) {
	id := strings.TrimSpace(mediaID)
	if id == "" {
		return "", fmt.Errorf("vidrift resolve: empty media id")
	}
	if isAnimeResolve(id, episode) {
		if episode.Episode > 0 {
			return fmt.Sprintf("anilist/%s/%d", id, episode.Episode), nil
		}
		return "anilist/" + id, nil
	}
	if episode.Season > 0 && episode.Episode > 0 {
		return fmt.Sprintf("tv/%s/%d/%d", id, episode.Season, episode.Episode), nil
	}
	return "movie/" + id, nil
}

// isAnimeResolve reports whether mediaID is an AniList id rather than a TMDB
// id. Anime episodes always carry an audio tag from their availability
// source; TMDB episodes reuse the series handle and never set one.
func isAnimeResolve(mediaID string, episode provider.Episode) bool {
	if strings.TrimSpace(episode.Audio) != "" {
		return true
	}
	if strings.HasPrefix(strings.TrimSpace(episode.ID), "watch/") {
		return true
	}
	if episode.TMDBID > 0 {
		if id, err := strconv.Atoi(strings.TrimSpace(mediaID)); err != nil || id != episode.TMDBID {
			return true
		}
	}
	return false
}

var (
	_ provider.Provider             = (*Client)(nil)
	_ provider.Presenter            = (*Client)(nil)
	_ provider.FeatureSource        = (*Client)(nil)
	_ provider.AudioLanguagesSource = (*Client)(nil)
)
