// Package pengu implements provider.Provider against the PenguPlay aggregator
// API (https://pengu.uk). PenguPlay is an aggregator that queries multiple
// streaming backends (4KHDHub, MovieBox, VegaMovies, VAPlayer, Miruro,
// VidFast, CineFreak, Cinejoy, KissKH, HDHub4u, Ani.pm, 2Peckle, Arctic,
// VixSrc, Vidy, ToonWorld4All, Reanime, PlayFy, MoviesDrives) and
// returns direct and HLS streams. Reanime, Ani.pm serve anime, ToonWorld4All serves
// cartoons and general media, Vidy, MoviesDrives, and 4KHDHub serve movies
// and TV shows, and live-sports serves live catalogs. VidLink and Arctic stay
// disabled because VidLink returns 428 errors and Arctic currently serves no streams.
// Anikoto stays disabled because Kari queries it natively.
package pengu

import (
	"bytes"
	"compress/flate"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"slices"
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
)

// log scopes every line from this package with its identity.
var pgLog = logging.With("provider", "pengu")

var penguAPIBase = config.PenguAPIBase

const penguUA = config.DesktopUserAgent

var (
	reResolution = regexp.MustCompile(`(?i)\b(4k|2160p|1440p|1080p|720p|480p|360p)\b`)
	reSourceTag  = regexp.MustCompile(`(?i)\b(4khdhub|moviebox|vegamovies|moviesdrives|miruro|cinefreak|vaplayer|vidlink|vidfast|cinejoy|hdghartv|kisskh|hdhub4u|anipm|ani\.pm|2peckle|debridscloud|5clover|daddylive|binged|streamedpk|cdnlive|timstreams|arctic|vixsrc|vidy|toonworld4all|toonworld|reanime|playfy|obejrzyj|lamovie|sololatino|moflix|premiumsmart)\b`)
	reAudioLang  = regexp.MustCompile(`(?i)(?:audio|language|languages):\s*([a-zA-Z, /]+)`)
	// reDubSubMarker matches MovieBox-style audio markers in filenames:
	// "{lang}dubaudio" (dubbed audio) vs "{lang}subaudio" (original audio
	// with subtitles) vs "OriginalAudio".
	reDubSubMarker  = regexp.MustCompile(`(?i)([a-z]+)(dub|sub)audio`)
	reQualityHeight = regexp.MustCompile(`(?i)\b(4k|2160|1080|720|480|360)p?\b`)
	// reHardSubRow / reHardDubRow match upstream burned-in subtitle
	// markers ("Hard Sub", "Hard Dub", "HardSub"). Ani.pm serves each
	// title in soft and hard variants; the markers are the only signal
	// telling them apart.
	reHardSubRow = regexp.MustCompile(`(?i)hard[\s_\-]*sub`)
	reHardDubRow = regexp.MustCompile(`(?i)hard[\s_\-]*dub`)
)

// liveCatalogs is the list of PenguPlay Stremio live sports and channel catalogs.
var liveCatalogs = []string{
	"pp-live-now",
	"pp-live-channels",
	"pp-live-upcoming",
	"pp-live-football",
	"pp-live-american_football",
	"pp-live-basketball",
	"pp-live-baseball",
	"pp-live-hockey",
	"pp-live-tennis",
	"pp-live-cricket",
	"pp-live-motorsport",
	"pp-live-rugby",
	"pp-live-mma",
	"pp-live-golf",
	"pp-live-darts",
	"pp-live-other",
}

// Client implements provider.Provider against the PenguPlay Stremio addon API.
type Client struct {
	base          *streambase.Base
	httpClient    *http.Client
	authToken     string
	configSegment string
	segAnime      string
	// anilistBase is the AniList GraphQL endpoint for anime search and
	// episode counts; config.AniListAPIBase in production, overrideable
	// in tests.
	anilistBase string
	// langFilter mirrors the user's audio-language settings (see
	// internal/settings LanguageFilter): nil or missing keys mean enabled.
	// It filters foreign dubbed audio at resolve time; original-audio and
	// English rows always pass.
	langFilter map[string]bool
	malMu      sync.Mutex
	malCache   map[int]int
}

type penguStreamItem struct {
	Name          string             `json:"name"`
	Title         string             `json:"title"`
	Description   string             `json:"description"`
	URL           string             `json:"url"`
	ExternalURL   string             `json:"externalUrl"`
	BehaviorHints penguBehaviorHints `json:"behaviorHints"`
	Subtitles     []penguSubtitle    `json:"subtitles"`
}

type penguBehaviorHints struct {
	Headers      map[string]string `json:"headers"`
	ProxyHeaders *penguProxyHeader `json:"proxyHeaders"`
	NotWebReady  bool              `json:"notWebReady"`
	Filename     string            `json:"filename"`
	VideoSize    int64             `json:"videoSize"`
}

type penguProxyHeader struct {
	Request map[string]string `json:"request"`
}

type penguSubtitle struct {
	ID   string `json:"id"`
	URL  string `json:"url"`
	Lang string `json:"lang"`
}

type penguResponse struct {
	Streams []penguStreamItem `json:"streams"`
	Error   string            `json:"error,omitempty"`
}

type penguCatalogResponse struct {
	Metas []penguCatalogMeta `json:"metas"`
	Error string             `json:"error,omitempty"`
}

type penguCatalogMeta struct {
	ID          string   `json:"id"`
	Type        string   `json:"type"`
	Name        string   `json:"name"`
	Poster      string   `json:"poster"`
	PosterShape string   `json:"posterShape"`
	Background  string   `json:"background"`
	ReleaseInfo string   `json:"releaseInfo"`
	Released    string   `json:"released"`
	Genres      []string `json:"genres"`
	Description string   `json:"description"`
}

// NewClient constructs the Pengu provider over the shared TMDB search base.
func NewClient(keyPool *tmdb.KeyPool, authToken string) (*Client, error) {
	return NewClientWithLanguageFilter(keyPool, authToken, nil)
}

// NewClientWithLanguageFilter is NewClient with the user's audio-language
// filter applied at resolve time (nil = everything enabled).
func NewClientWithLanguageFilter(keyPool *tmdb.KeyPool, authToken string, langFilter map[string]bool) (*Client, error) {
	base, err := streambase.New(keyPool)
	if err != nil {
		return nil, err
	}
	token := strings.TrimSpace(authToken)
	segment, err := buildConfigSegment(token)
	if err != nil {
		return nil, err
	}
	segAnime, err := buildAnimeConfigSegment(token)
	if err != nil {
		return nil, err
	}
	return &Client{
		base:          base,
		httpClient:    httpclient.NewWithUserAgentAndTimeout(penguUA, 60*time.Second),
		authToken:     token,
		configSegment: segment,
		segAnime:      segAnime,
		anilistBase:   config.AniListAPIBase,
		malCache:      make(map[int]int),
		langFilter:    langFilter,
	}, nil
}

// languageEnabled mirrors the TUI toggle semantics: a nil filter or a
// missing key means enabled; only an explicit false disables.
func (c *Client) languageEnabled(code, display string) bool {
	if c.langFilter == nil {
		return true
	}
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

// Alias implements provider.Presenter.
func (c *Client) Alias() string { return "Pengu" }

// Name implements Provider.
func (c *Client) Name() string { return "pengu" }

// Modes implements Provider.
func (c *Client) Modes() []provider.Mode {
	return []provider.Mode{
		{Name: provider.ModeMovies, Priority: 2},
		{Name: provider.ModeTV, Priority: 2},
		{Name: provider.ModeCartoon, Priority: 2},
		// Anime resolves through the anime config segment (Ani.pm,
		// Reanime, Miruro, KissKH, 2Peckle, MovieBox). Priority
		// trails the direct scrapers: same-height ties prefer direct CDN
		// rows over proxied ones, while Pengu still adds fallback depth.
		{Name: provider.ModeAnime, Priority: 3},
		{Name: provider.ModeLive, Priority: 1},
	}
}

// Features implements provider.FeatureSource.
func (c *Client) Features(mode provider.ContentType) provider.Features {
	if mode == provider.ModeAnime {
		return provider.Features{AudioSelection: true}
	}
	if mode == provider.ModeLive {
		return provider.Features{
			AllowEmptyQuery:   true,
			NoCachedSearches:  true,
			SearchPlaceholder: "Search live sports & channels…",
		}
	}
	return provider.Features{}
}

// audioLanguages is the full set of audio-track languages the PenguPlay API
// aggregates across its internal providers.
var audioLanguages = []provider.AudioLanguage{
	{Code: "en", Display: "English"},
	{Code: "hi", Display: "Hindi"},
	{Code: "ta", Display: "Tamil"},
	{Code: "te", Display: "Telugu"},
	{Code: "ko", Display: "Korean"},
	{Code: "ja", Display: "Japanese"},
	{Code: "zh", Display: "Chinese"},
	{Code: "es", Display: "Spanish"},
	{Code: "fr", Display: "French"},
	{Code: "de", Display: "German"},
	{Code: "it", Display: "Italian"},
	{Code: "pt", Display: "Portuguese"},
	{Code: "ru", Display: "Russian"},
	{Code: "ar", Display: "Arabic"},
	{Code: "th", Display: "Thai"},
	{Code: "vi", Display: "Vietnamese"},
	{Code: "ms", Display: "Malay"},
	{Code: "id", Display: "Indonesian"},
	{Code: "uk", Display: "Ukrainian"},
}

// AudioLanguages implements provider.AudioLanguagesSource.
func (c *Client) AudioLanguages() []provider.AudioLanguage {
	return audioLanguages
}

// Search delegates to TMDB search base for Movies/TV/Cartoons, to the
// shared AniList index for Anime, and queries PenguPlay's live sports
// catalogs when in ModeLive.
func (c *Client) Search(ctx context.Context, query string, mode provider.ContentType) ([]provider.SearchResult, error) {
	if mode == provider.ModeLive {
		return c.searchLive(ctx, query)
	}
	if mode == provider.ModeAnime {
		// Shared AniList index (cached + singleflighted across every
		// anime provider), so joining the fan-out costs no extra
		// upstream search. Bare AniList IDs dedupe against the direct
		// providers' rows in the merged list.
		if strings.TrimSpace(query) == "" {
			return nil, provider.ErrNoResults
		}
		results, err := anilist.SearchWithEndpoint(ctx, c.httpClient, query, c.anilistBase)
		if err != nil {
			return nil, fmt.Errorf("pengu search: %w", err)
		}
		if len(results) == 0 {
			return nil, provider.ErrNoResults
		}
		return results, nil
	}
	return c.base.Search(ctx, query, mode)
}

// FetchEpisodes delegates to the shared TMDB-keyed base for series, or returns
// a single direct episode handle for live sports and channels.
func (c *Client) FetchEpisodes(ctx context.Context, series provider.SearchResult) ([]provider.Episode, error) {
	if series.Type == provider.ModeLive || strings.HasPrefix(series.ID, "pp-live:") {
		return []provider.Episode{
			{
				Title: series.Title,
				ID:    series.ID,
			},
		}, nil
	}
	if series.Type == provider.ModeAnime || strings.HasPrefix(series.ID, "anilist:") {
		return c.fetchAnimeEpisodes(ctx, series)
	}
	return c.base.FetchEpisodes(ctx, series)
}

// fetchAnimeEpisodes synthesizes the episode list from AniList metadata:
// Absolute episode numbers in broadcast order. Audio stays unset so the
// sub/dub filter passes every row and the choice surfaces at the source
// level (Sub / Dub / Hard Sub / Hard Dub variants per backend). Episode
// IDs carry the anilist: prefix so resolution addresses Pengu's anime
// catalog directly.
func (c *Client) fetchAnimeEpisodes(ctx context.Context, series provider.SearchResult) ([]provider.Episode, error) {
	id, ok := parseAnilistID(series.ID)
	if !ok {
		return nil, fmt.Errorf("pengu episodes: invalid anime id %q: %w", series.ID, provider.ErrNoEpisodes)
	}
	media, err := anilist.FetchMediaWithEndpoint(ctx, c.httpClient, id, c.anilistBase)
	if err != nil {
		return nil, fmt.Errorf("pengu episodes: %w", err)
	}
	if media.IDMal > 0 {
		c.malMu.Lock()
		if c.malCache == nil {
			c.malCache = make(map[int]int)
		}
		c.malCache[id] = media.IDMal
		c.malMu.Unlock()
	}
	if strings.EqualFold(strings.TrimSpace(media.Format), "MOVIE") {
		return []provider.Episode{{
			Title: series.Title,
			ID:    fmt.Sprintf("anilist:%d", id),
		}}, nil
	}
	count := media.EpisodeCount()
	if count <= 0 {
		return nil, fmt.Errorf("pengu episodes: unknown episode count for %q: %w", series.Title, provider.ErrNoEpisodes)
	}
	eps := make([]provider.Episode, 0, count)
	for n := 1; n <= count; n++ {
		eps = append(eps, provider.Episode{
			Season:  1,
			Episode: n,
			Title:   fmt.Sprintf("Episode %d", n),
			ID:      fmt.Sprintf("anilist:%d:%d", id, n),
		})
	}
	return eps, nil
}

// parseAnilistID extracts the AniList numeric ID from a bare ("21") or
// prefixed ("anilist:21", "anilist:21:1180") handle.
func parseAnilistID(handle string) (int, bool) {
	s := strings.TrimSpace(handle)
	s = strings.TrimPrefix(s, "anilist:")
	if i := strings.Index(s, ":"); i >= 0 {
		s = s[:i]
	}
	id, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil || id <= 0 {
		return 0, false
	}
	return id, true
}

// malID returns the MyAnimeList ID for an AniList media ID, consulting the
// client cache before querying AniList GraphQL. Returns 0 on failure.
func (c *Client) malID(ctx context.Context, anilistID int) int {
	c.malMu.Lock()
	if c.malCache == nil {
		c.malCache = make(map[int]int)
	}
	if malID, ok := c.malCache[anilistID]; ok {
		c.malMu.Unlock()
		return malID
	}
	c.malMu.Unlock()

	media, err := anilist.FetchMediaWithEndpoint(ctx, c.httpClient, anilistID, c.anilistBase)
	if err == nil && media.IDMal > 0 {
		c.malMu.Lock()
		if c.malCache == nil {
			c.malCache = make(map[int]int)
		}
		c.malCache[anilistID] = media.IDMal
		c.malMu.Unlock()
		return media.IDMal
	}
	return 0
}

// animeStremioID builds the Stremio series ID Pengu's anime catalog wants:
// anilist:{id}:{absoluteEpisode} for episodes (the absolute form returns
// the full variant set including dubs, verified live), anilist:{id} for
// movies. The AniList ID comes from either handle; bare numeric anime
// handles are always AniList IDs because every anime provider is
// AniList-keyed.
func animeStremioID(mediaID string, episode provider.Episode) (string, bool) {
	id, ok := parseAnilistID(mediaID)
	if !ok {
		id, ok = parseAnilistID(episode.ID)
	}
	if !ok {
		return "", false
	}
	if episode.Season <= 0 && episode.Episode <= 0 {
		return fmt.Sprintf("anilist:%d", id), true
	}
	if episode.Episode <= 0 {
		return "", false
	}
	return fmt.Sprintf("anilist:%d:%d", id, episode.Episode), true
}

// ResolveSource resolves playable sources for the given media ID and episode,
// ignoring Movy.sx (which is handled independently in Kari) and collapsing
// only exact mirrors of the same encode.
func (c *Client) ResolveSource(ctx context.Context, mediaID string, episode provider.Episode) ([]provider.MediaSource, error) {
	if strings.HasPrefix(mediaID, "pp-live:") || strings.HasPrefix(episode.ID, "pp-live:") {
		stremioID := mediaID
		if !strings.HasPrefix(stremioID, "pp-live:") && strings.HasPrefix(episode.ID, "pp-live:") {
			stremioID = episode.ID
		}
		return c.resolveLiveSource(ctx, stremioID)
	}

	isAnime := episode.Audio != "" || strings.HasPrefix(episode.ID, "watch/") ||
		strings.HasPrefix(mediaID, "anilist:") || strings.HasPrefix(episode.ID, "anilist:") ||
		strings.HasPrefix(mediaID, "mal:") || strings.HasPrefix(episode.ID, "mal:")
	if isAnime {
		mediaType := provider.MediaTypeMovie
		if episode.Season > 0 || episode.Episode > 0 {
			mediaType = provider.MediaTypeTV
		}

		// Try MAL-based Stremio ID first because Pengu's anime scrapers
		// (Ani.pm, Reanime, 2Peckle) natively index by MyAnimeList ID,
		// answering in ~1s whereas anilist: mapping often returns 0 streams
		// or takes 5-10s upstream. Fall back to anilist: if mal: returns no sources.
		anilistID, ok := parseAnilistID(mediaID)
		if !ok {
			anilistID, ok = parseAnilistID(episode.ID)
		}
		if !ok && strings.HasPrefix(episode.ID, "watch/") {
			h := kit.ParseHandle(mediaID, episode.ID, episode.Audio, episode.Episode, "")
			if n, err := strconv.Atoi(h.AniListID); err == nil && n > 0 {
				anilistID, ok = n, true
			}
		}
		if ok && anilistID > 0 {
			malID := c.malID(ctx, anilistID)
			if malID > 0 {
				var stremioID string
				if episode.Season <= 0 && episode.Episode <= 0 {
					stremioID = fmt.Sprintf("mal:%d", malID)
				} else if episode.Episode > 0 {
					stremioID = fmt.Sprintf("mal:%d:%d", malID, episode.Episode)
				}
				if stremioID != "" {
					sources, err := c.resolveStreams(ctx, mediaType, stremioID, 0, true)
					if err == nil && len(sources) > 0 {
						return sources, nil
					}
				}
			}
		}

		stremioID, ok := animeStremioID(mediaID, episode)
		if !ok {
			return nil, fmt.Errorf("pengu: cannot resolve anime id %q", mediaID)
		}
		return c.resolveStreams(ctx, mediaType, stremioID, 0, true)
	}

	tmdbID := episode.TMDBID
	if tmdbID <= 0 {
		var err error
		tmdbID, err = strconv.Atoi(mediaID)
		if err != nil {
			return nil, fmt.Errorf("invalid media ID: %w", err)
		}
	}

	mediaType := provider.MediaTypeMovie
	stremioID := fmt.Sprintf("tmdb:%d", tmdbID)
	if episode.Season > 0 || episode.Episode > 0 {
		mediaType = provider.MediaTypeTV
		stremioID = fmt.Sprintf("tmdb:%d:%d:%d", tmdbID, episode.Season, episode.Episode)
	}

	return c.resolveStreams(ctx, mediaType, stremioID, tmdbID, false)
}

func (c *Client) resolveLiveSource(ctx context.Context, stremioID string) ([]provider.MediaSource, error) {
	resp, err := c.fetchPenguStreams(ctx, provider.MediaTypeTV, stremioID, false)
	if err != nil {
		return nil, err
	}
	if len(resp.Streams) == 0 {
		return nil, provider.ErrNoSources
	}
	if isAuthPrompt(resp.Streams) {
		pgLog.Warn("pengu authentication required or expired", "id", stremioID)
		return nil, fmt.Errorf("pengu: %w", provider.ErrAuthRequired)
	}

	seenURLs := make(map[string]bool)
	sources := make([]provider.MediaSource, 0, len(resp.Streams))
	for _, stream := range resp.Streams {
		if strings.TrimSpace(stream.URL) == "" || isHousekeepingEntry(stream) || isExcludedStream(stream) {
			continue
		}
		if seenURLs[stream.URL] {
			continue
		}
		seenURLs[stream.URL] = true
		sources = append(sources, parseStreamItem(stream))
	}
	if len(sources) == 0 {
		return nil, provider.ErrNoSources
	}
	return sources, nil
}

func (c *Client) resolveStreams(ctx context.Context, mediaType, stremioID string, tmdbID int, isAnime bool) ([]provider.MediaSource, error) {
	// Pengu's separate subtitle resource is fast and should finish while its
	// stream aggregator is resolving. Bound the wait so a slow subtitle index
	// cannot hold up otherwise playable sources.
	subtitleCtx, cancelSubtitles := context.WithTimeout(ctx, 2*time.Second)
	defer cancelSubtitles()
	type subtitleResult struct {
		options []provider.SubtitleOption
	}
	subtitleResults := make(chan subtitleResult, 1)
	go func() {
		subtitleResults <- subtitleResult{options: c.fetchPenguSubtitles(subtitleCtx, mediaType, stremioID, isAnime)}
	}()

	resp, err := c.fetchPenguStreams(ctx, mediaType, stremioID, isAnime)
	if err != nil {
		return nil, err
	}
	if len(resp.Streams) == 0 {
		return nil, provider.ErrNoSources
	}

	// Check if the only returned stream is an auth prompt
	if isAuthPrompt(resp.Streams) {
		pgLog.Warn("pengu authentication required or expired", "tmdbID", tmdbID)
		return nil, fmt.Errorf("pengu: %w", provider.ErrAuthRequired)
	}

	seenURLs := make(map[string]bool)
	seenGroup := make(map[string]bool)
	var kept, dropped []resolveCandidate

	for _, stream := range resp.Streams {
		if strings.TrimSpace(stream.URL) == "" || isHousekeepingEntry(stream) {
			continue
		}
		// Ignore Movy.sx (maintained independently in Kari).
		if isExcludedStream(stream) {
			continue
		}
		if seenURLs[stream.URL] {
			continue
		}
		seenURLs[stream.URL] = true

		source := parseStreamItem(stream)
		// Collapse only exact mirrors: distinct encodes from the same backend
		// carry different filenames/sizes and are kept as separate choices.
		groupKey := fmt.Sprintf("%s|%s|%s|%s|%d",
			source.Quality, source.Language, source.Type,
			stream.BehaviorHints.Filename, stream.BehaviorHints.VideoSize)
		if seenGroup[groupKey] {
			continue
		}
		seenGroup[groupKey] = true

		// Foreign-language audio the user didn't enable is dropped here
		// so it never floods the picker; original-audio, English, enabled
		// languages, and undetectable tracks always pass.
		trackLang := streamAudioLanguage(stream)
		if trackLang == "" {
			trackLang = "en"
		}
		if (!isAnime || trackLang != "ja") && !c.languageEnabled(trackLang, lang.Name(trackLang)) {
			dropped = append(dropped, resolveCandidate{source: source, stream: stream})
			continue
		}
		kept = append(kept, resolveCandidate{source: source, stream: stream})
	}

	candidates := kept
	if len(candidates) == 0 && len(dropped) > 0 {
		candidates = dropped
	}
	sources := collapseMirrors(candidates)
	if len(sources) == 0 {
		return nil, provider.ErrNoSources
	}
	select {
	case result := <-subtitleResults:
		for i := range sources {
			sources[i].Subtitles = mergeSubtitleOptions(sources[i].Subtitles, result.options)
		}
	case <-subtitleCtx.Done():
	}
	return sources, nil
}

func (c *Client) fetchPenguSubtitles(ctx context.Context, mediaType, stremioID string, isAnime bool) []provider.SubtitleOption {
	resourceType := "series"
	if mediaType == provider.MediaTypeMovie {
		resourceType = "movie"
	}
	segment := c.configSegment
	if isAnime && c.segAnime != "" {
		segment = c.segAnime
	}
	target := fmt.Sprintf("%s/%s/subtitles/%s/%s.json", penguAPIBase, segment, resourceType, stremioID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		pgLog.Debug("subtitle request construction failed", "err", err)
		return nil
	}
	req.Header.Set("User-Agent", penguUA)
	req.Header.Set("Accept", "application/json")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		if ctx.Err() == nil {
			pgLog.Debug("subtitle request failed", "err", redactRequestError(err, c.configSegment))
		}
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		pgLog.Debug("subtitle request returned non-200", "status", resp.StatusCode)
		return nil
	}
	body, err := httpclient.ReadCapped(resp)
	if err != nil {
		pgLog.Debug("subtitle response read failed", "err", err)
		return nil
	}
	var payload struct {
		Subtitles []penguSubtitle `json:"subtitles"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		pgLog.Debug("subtitle response decode failed", "err", err)
		return nil
	}

	seen := make(map[string]struct{}, len(payload.Subtitles))
	options := make([]provider.SubtitleOption, 0, len(payload.Subtitles))
	for _, track := range payload.Subtitles {
		u := strings.TrimSpace(track.URL)
		if u == "" {
			continue
		}
		if _, ok := seen[u]; ok {
			continue
		}
		seen[u] = struct{}{}
		language := lang.Normalize(track.Lang)
		if language == "" {
			language = "und"
		}
		options = append(options, provider.SubtitleOption{URL: u, Language: language})
	}
	return options
}

func mergeSubtitleOptions(current, additional []provider.SubtitleOption) []provider.SubtitleOption {
	seen := make(map[string]struct{}, len(current)+len(additional))
	for _, track := range current {
		seen[track.URL] = struct{}{}
	}
	for _, track := range additional {
		if _, ok := seen[track.URL]; ok {
			continue
		}
		current = append(current, track)
		seen[track.URL] = struct{}{}
	}
	return current
}

// resolveCandidate pairs a parsed source with its raw stream for the
// mirror-collapse pass.
type resolveCandidate struct {
	source provider.MediaSource
	stream penguStreamItem
}

// collapseMirrors drops same-file mirrors that differ only by signed URL:
// rows sharing backend, height, language, container, and a known nonzero
// size keep the most capable container (progressive mp4 over manifests).
func collapseMirrors(candidates []resolveCandidate) []provider.MediaSource {
	// Stable-sort candidates so progressive MP4 (rank 0) and HLS (rank 1)
	// precede fragile DASH manifests (rank 2), preserving upstream order
	// within each container tier.
	slices.SortStableFunc(candidates, func(a, b resolveCandidate) int {
		return containerRank(streamContainer(a.source)) - containerRank(streamContainer(b.source))
	})

	type mirrorKey struct {
		backend string
		height  int
		lang    string
		subType string
		size    int64
	}
	bestIdx := make(map[mirrorKey]int)
	sources := make([]provider.MediaSource, 0, len(candidates))
	for _, cand := range candidates {
		key := mirrorKey{
			backend: mirrorBackend(cand.source.Quality),
			height:  qualityHeight(cand.source.Quality),
			lang:    cand.source.Language,
			subType: cand.source.SubType,
			size:    cand.stream.BehaviorHints.VideoSize,
		}
		if key.size <= 0 {
			sources = append(sources, cand.source)
			continue
		}
		if idx, ok := bestIdx[key]; ok {
			if containerRank(streamContainer(cand.source)) < containerRank(streamContainer(sources[idx])) {
				sources[idx] = cand.source
			}
			continue
		}
		bestIdx[key] = len(sources)
		sources = append(sources, cand.source)
	}
	return sources
}

// mirrorBackend extracts the [Backend] token from a quality label such as
// "1080p [MovieBox] (Hindi)".
func mirrorBackend(quality string) string {
	open := strings.Index(quality, "[")
	if open < 0 {
		return ""
	}
	rest := quality[open+1:]
	close := strings.Index(rest, "]")
	if close <= 0 {
		return ""
	}
	return strings.TrimSpace(rest[:close])
}

// qualityHeight extracts the vertical resolution from a quality label
// ("1080p …" → 1080, "4K …" → 2160); unknown labels sort as 0.
func qualityHeight(quality string) int {
	m := reQualityHeight.FindString(quality)
	if m == "" {
		return 0
	}
	if strings.EqualFold(m, "4k") {
		return 2160
	}
	n, _ := strconv.Atoi(strings.TrimSuffix(strings.ToLower(m), "p"))
	return n
}

// streamContainer classifies a source for mirror preference: progressive
// mp4 beats HLS manifests beats DASH manifests.
func streamContainer(src provider.MediaSource) string {
	l := strings.ToLower(src.URL + " " + src.Type)
	switch {
	case strings.Contains(l, ".mpd") || strings.Contains(l, "/dash/"):
		return "dash"
	case strings.Contains(l, ".m3u8") || src.Type == provider.SourceTypeHLS:
		return "hls"
	default:
		return "mp4"
	}
}

func containerRank(container string) int {
	switch container {
	case "mp4":
		return 0
	case "hls":
		return 1
	case "dash":
		return 2
	default:
		return 3
	}
}

func (c *Client) fetchCatalog(ctx context.Context, catID, query string) ([]penguCatalogMeta, error) {
	reqURL := fmt.Sprintf("%s/%s/catalog/tv/%s.json", penguAPIBase, c.configSegment, catID)
	if strings.TrimSpace(query) != "" {
		reqURL = fmt.Sprintf("%s/%s/catalog/tv/%s/search=%s.json", penguAPIBase, c.configSegment, catID, url.QueryEscape(query))
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("pengu catalog request: %w", err)
	}
	req.Header.Set("User-Agent", penguUA)
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("pengu do catalog request: %w", redactRequestError(err, c.configSegment))
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusTooManyRequests {
		return nil, fmt.Errorf("pengu: %w", provider.ErrRateLimited)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, &provider.HTTPError{Code: resp.StatusCode}
	}

	body, err := httpclient.ReadCapped(resp)
	if err != nil {
		return nil, fmt.Errorf("pengu read catalog body: %w", err)
	}

	var payload penguCatalogResponse
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("pengu decode catalog response: %w", err)
	}
	if payload.Error == "rate_limited" {
		return nil, fmt.Errorf("pengu: %w", provider.ErrRateLimited)
	}

	return payload.Metas, nil
}

func (c *Client) searchLive(ctx context.Context, query string) ([]provider.SearchResult, error) {
	trimmedQuery := strings.TrimSpace(query)
	catalogs := liveCatalogs
	if trimmedQuery == "" {
		// When browsing without a query, return live matches and 24/7 channels
		// so only content available right now is shown.
		catalogs = []string{"pp-live-now", "pp-live-channels"}
	}
	type catResult struct {
		index int
		metas []penguCatalogMeta
		err   error
	}

	resChan := make(chan catResult, len(catalogs))

	for i, cat := range catalogs {
		go func(idx int, catalogID string) {
			metas, err := c.fetchCatalog(ctx, catalogID, trimmedQuery)
			// The channel is buffered to len(catalogs), so an abandoned
			// straggler (caller returned on ctx.Done) never blocks.
			resChan <- catResult{index: idx, metas: metas, err: err}
		}(i, cat)
	}

	// Collect results indexed by catalog order to preserve priority (pp-live-now > channels > upcoming > sports).
	ordered := make([][]penguCatalogMeta, len(catalogs))
	var rateLimited bool
	var hadSuccess bool
	for remaining := len(catalogs); remaining > 0; {
		select {
		case r := <-resChan:
			remaining--
			if r.err == nil {
				ordered[r.index] = r.metas
				hadSuccess = true
			} else if errors.Is(r.err, provider.ErrRateLimited) {
				rateLimited = true
			}
		case <-ctx.Done():
			// Never let a slow catalog (the channel list is ~1.5 MB) sink
			// the fast live-now catalog: return whatever already arrived
			// instead of waiting for every goroutine.
			remaining = 0
		}
	}

	if !hadSuccess && rateLimited {
		return nil, fmt.Errorf("pengu: %w", provider.ErrRateLimited)
	}

	seenIDs := make(map[string]bool)
	var results []provider.SearchResult

	for i, metas := range ordered {
		for _, m := range metas {
			if m.ID == "" || seenIDs[m.ID] {
				continue
			}
			seenIDs[m.ID] = true

			title := cleanEmojis(m.Name)
			if title == "" {
				title = m.ID
			}

			var genres []string
			for _, g := range m.Genres {
				if cg := cleanEmojis(g); cg != "" {
					genres = append(genres, cg)
				}
			}

			results = append(results, liveResult(catalogs[i], m, title, genres))
		}
	}
	if len(results) == 0 {
		return nil, provider.ErrNoResults
	}

	return results, nil
}

// liveResult maps one catalog entry to a search result using the explicit
// scheduling fields: the catalog decides live-now vs channels, and a
// parseable releaseInfo becomes StartsAt. Schedule text never goes into
// Year.
func liveResult(catalogID string, m penguCatalogMeta, title string, genres []string) provider.SearchResult {
	now := time.Now()
	res := provider.SearchResult{
		Title:     title,
		ID:        m.ID,
		Type:      provider.ModeLive,
		MediaType: provider.MediaTypeLive,
		CoverURL:  m.Poster,
		Overview:  cleanEmojis(m.Description),
		Genres:    genres,
		StartsAt:  parseLiveStart(m.ReleaseInfo, m.Released),
	}
	switch catalogID {
	case "pp-live-channels":
		res.Group = "Channel"
		res.Live = true // 24/7 channels are always live
	case "pp-live-now":
		res.Live = true
	default:
		// Upcoming catalogs: an event whose start already passed is live.
		res.Live = !res.StartsAt.IsZero() && !res.StartsAt.After(now)
	}
	return res
}

// reLiveTZ matches the upstream releaseInfo shapes: a local datetime
// with an optional GMT offset suffix, e.g. "2026-09-21 21:00 GMT+5:30".
var reLiveTZ = regexp.MustCompile(`^(\d{4}-\d{2}-\d{2} \d{2}:\d{2}(?::\d{2})?)(?:\s+GMT\s*([+-])(\d{1,2}):?(\d{2}))?\s*$`)

// parseLiveStart parses the catalog releaseInfo timestamp into local
// time; zero when it carries no parseable schedule ("24/7" channels,
// blanks). A GMT offset suffix is honored so a 21:00 GMT+5:30 event
// sorts into Today/Tomorrow correctly instead of vanishing.
func parseLiveStart(releaseInfo, released string) time.Time {
	if rel := strings.TrimSpace(released); rel != "" {
		for _, layout := range []string{time.RFC3339, time.RFC3339Nano} {
			if t, err := time.Parse(layout, rel); err == nil {
				return t.In(time.Local)
			}
		}
	}
	info := strings.TrimSpace(cleanEmojis(releaseInfo))
	for _, layout := range []string{"2006-01-02 15:04", "2006-01-02 15:04:05", time.RFC3339} {
		if t, err := time.ParseInLocation(layout, info, time.Local); err == nil {
			return t
		}
	}
	m := reLiveTZ.FindStringSubmatch(info)
	if m == nil {
		return time.Time{}
	}
	layout := "2006-01-02 15:04"
	if len(m[1]) > 16 {
		layout = "2006-01-02 15:04:05"
	}
	if m[2] == "" {
		if t, err := time.ParseInLocation(layout, m[1], time.Local); err == nil {
			return t
		}
		return time.Time{}
	}
	hours, _ := strconv.Atoi(m[3])
	mins, _ := strconv.Atoi(m[4])
	offset := hours*3600 + mins*60
	if m[2] == "-" {
		offset = -offset
	}
	base, err := time.ParseInLocation(layout, m[1], time.UTC)
	if err != nil {
		return time.Time{}
	}
	return base.Add(-time.Duration(offset) * time.Second).In(time.Local)
}

// isExcludedStream checks if a stream originated from a backend Kari
// maintains natively (Movy.sx, Anikoto) or ignores due to incorrect source
// mappings (Ani.pm).
func isExcludedStream(stream penguStreamItem) bool {
	combined := strings.ToLower(strings.Join([]string{
		stream.Name,
		stream.Title,
		stream.Description,
		stream.URL,
		stream.BehaviorHints.Filename,
	}, " "))
	if strings.Contains(combined, ".mpd") {
		return true
	}
	return strings.Contains(combined, "movysx") || strings.Contains(combined, "movy.sx") ||
		strings.Contains(combined, "anikoto") ||
		strings.Contains(combined, "ani.pm") || strings.Contains(combined, "anipm") ||
		strings.Contains(combined, "111477") || strings.Contains(combined, "cno.to")
}

// isHousekeepingEntry reports whether a stream item is one of Pengu's own
// non-playable rows (donation banner, announcements) rather than a media
// source. The donation banner carries an externalUrl and no playable url.
func isHousekeepingEntry(stream penguStreamItem) bool {
	if stream.ExternalURL != "" && strings.TrimSpace(stream.URL) == "" {
		return true
	}
	combined := strings.ToLower(stream.Name + " " + stream.Title + " " + stream.Description)
	return strings.Contains(combined, "support the project") ||
		strings.Contains(combined, "pengu.uk/donate")
}

// isAuthPrompt checks if the response is Pengu's authentication requirement notice.
func isAuthPrompt(streams []penguStreamItem) bool {
	if len(streams) != 1 {
		return false
	}
	s := streams[0]
	return strings.Contains(s.URL, "signin.mp4") ||
		strings.Contains(strings.ToLower(s.Title), "sign in") ||
		strings.Contains(strings.ToLower(s.Description), "authentication is missing")
}

// buildConfigSegment compiles the default (movies/TV) multi-provider
// configuration into a compact raw-deflated base64url segment expected by PenguPlay.
// Movy.sx is deliberately omitted because Kari manages Movy.sx independently.
// Anime-focused backends (reanime, anipm, miruro) stay off here; anikoto also
// stays off because Kari queries it natively. Anime requests use
// buildAnimeConfigSegment instead.
func buildConfigSegment(token string) (string, error) {
	cfg := map[string]any{
		"source_4khdhub":       "on",
		"source_moviebox":      "on",
		"source_vegamovies":    "on",
		"source_moviesdrives":  "on",
		"source_vaplayer":      "on",
		"source_miruro":        "off",
		"source_vidlink":       "off",
		"source_vidfast":       "on",
		"source_cinefreak":     "on",
		"source_cinejoy":       "on",
		"source_kisskh":        "on",
		"source_hdhub4u":       "on",
		"source_anikoto":       "off",
		"source_anipm":         "off",
		"source_2peckle":       "on",
		"source_vidy":          "on",
		"source_obejrzyj":      "on",
		"source_arctic":        "off",
		"source_toonworld4all": "on",
		"source_reanime":       "off",
		"source_lamovie":       "on",
		"source_sololatino":    "on",
		"source_moflix":        "on",
		"source_premiumsmart":  "on",
		"source_live-sports":   "on",
		"res_2160":             "on",
		"res_1080":             "on",
		"res_720":              "on",
		"res_480":              "on",
		"res_360":              "on",
	}
	if token != "" {
		cfg["auth_token"] = token
	}
	return encodeConfig(cfg)
}

// buildAnimeConfigSegment compiles the anime-only multi-provider
// configuration. Anikoto stays off because Kari queries it natively.
func buildAnimeConfigSegment(token string) (string, error) {
	cfg := map[string]any{
		"source_reanime":       "on",
		"source_anikoto":       "off",
		"source_anipm":         "off",
		"source_miruro":        "off",
		"source_kisskh":        "off",
		"source_2peckle":       "on",
		"source_moviebox":      "off",
		"source_4khdhub":       "off",
		"source_vegamovies":    "off",
		"source_moviesdrives":  "off",
		"source_vaplayer":      "off",
		"source_vidlink":       "off",
		"source_vidfast":       "off",
		"source_cinefreak":     "off",
		"source_cinejoy":       "off",
		"source_hdhub4u":       "off",
		"source_vidy":          "off",
		"source_toonworld4all": "off",
		"source_arctic":        "off",
		"source_live-sports":   "off",
		"res_2160":             "on",
		"res_1080":             "on",
		"res_720":              "on",
		"res_480":              "on",
		"res_360":              "on",
	}
	if token != "" {
		cfg["auth_token"] = token
	}
	return encodeConfig(cfg)
}

func encodeConfig(cfg map[string]any) (string, error) {
	data, err := json.Marshal(cfg)
	if err != nil {
		return "", fmt.Errorf("pengu marshal config: %w", err)
	}

	var buf bytes.Buffer
	fw, err := flate.NewWriter(&buf, flate.BestCompression)
	if err != nil {
		return "", fmt.Errorf("pengu flate writer: %w", err)
	}
	if _, err := fw.Write(data); err != nil {
		return "", fmt.Errorf("pengu deflate: %w", err)
	}
	if err := fw.Close(); err != nil {
		return "", fmt.Errorf("pengu close flate: %w", err)
	}

	return "z" + base64.RawURLEncoding.EncodeToString(buf.Bytes()), nil
}

// fetchPenguStreams issues the Stremio stream request against the Pengu endpoint.
func (c *Client) fetchPenguStreams(ctx context.Context, mediaType, stremioID string, isAnime bool) (*penguResponse, error) {
	pgLog.Debug("fetch start", "mediaType", mediaType, "id", stremioID, "isAnime", isAnime)

	stremioType := "movie"
	if strings.HasPrefix(stremioID, "pp-live:") || mediaType == provider.MediaTypeLive {
		stremioType = "tv"
	} else if mediaType == provider.MediaTypeTV {
		stremioType = "series"
	}
	segment := c.configSegment
	if isAnime && c.segAnime != "" {
		segment = c.segAnime
	}
	url := fmt.Sprintf("%s/%s/stream/%s/%s.json", penguAPIBase, segment, stremioType, stremioID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("pengu request: %w", err)
	}
	req.Header.Set("User-Agent", penguUA)
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		if strings.Contains(err.Error(), "giving up after") || strings.Contains(err.Error(), "429") {
			pgLog.Warn("pengu rate limited", "mediaType", mediaType, "id", stremioID)
			return nil, fmt.Errorf("pengu: %w", provider.ErrRateLimited)
		}
		return nil, fmt.Errorf("pengu do request: %w", redactRequestError(err, segment))
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusTooManyRequests {
		pgLog.Warn("pengu rate limited", "mediaType", mediaType, "id", stremioID)
		return nil, fmt.Errorf("pengu: %w", provider.ErrRateLimited)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, &provider.HTTPError{Code: resp.StatusCode}
	}

	body, err := httpclient.ReadCapped(resp)
	if err != nil {
		return nil, fmt.Errorf("pengu read body: %w", err)
	}

	var payload penguResponse
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("pengu decode response: %w", err)
	}

	if payload.Error == "rate_limited" {
		pgLog.Warn("pengu rate limited", "mediaType", mediaType, "id", stremioID)
		return nil, fmt.Errorf("pengu: %w", provider.ErrRateLimited)
	}

	pgLog.Debug("fetch success", "mediaType", mediaType, "id", stremioID, "streams", len(payload.Streams))
	return &payload, nil
}

// redactRequestError prevents the encoded configuration segment, which can
// contain a user's Pengu auth token, from escaping through transport errors.
func redactRequestError(err error, configSegment string) error {
	message := err.Error()
	if configSegment != "" {
		message = strings.ReplaceAll(message, configSegment, "[redacted]")
	}
	return errors.New(message)
}

// parseStreamItem extracts quality, headers, subtitles, and audio language from a stream entry.
func parseStreamItem(stream penguStreamItem) provider.MediaSource {
	combined := stream.Name + " " + stream.Title + " " + stream.Description + " " + stream.BehaviorHints.Filename

	// 1. Resolution
	res := "1080p"
	if m := reResolution.FindString(combined); m != "" {
		upper := strings.ToUpper(m)
		if upper == "4K" || upper == "2160P" {
			res = "4K"
		} else {
			res = strings.ToLower(m)
		}
	} else if strings.Contains(strings.ToLower(combined), "auto") {
		res = "Auto"
	} else if strings.Contains(strings.ToLower(combined), "live") {
		res = "Live"
	}

	// 2. Source Provider Tag
	sourceName := ""
	if m := reSourceTag.FindString(combined); m != "" {
		sourceName = canonProviderName(m)
	}

	// 3. Audio Language. MovieBox-style "{lang}dubaudio"/"{lang}subaudio"
	// filename markers are an extra signal for codes full-text detection
	// misses (e.g. "esla", "ptbr"). Ani.pm rows use a restricted detector:
	// their "📝 Subtitles: English, French, …" list names subtitle tracks,
	// and the shared full-text scan mistakes one for dub audio (a soft-sub
	// row labeled "(French)" that a French-audio filter would then drop).
	// Only the explicit Audio:/Language: line and filename markers count.
	audioLang := streamAudioLanguage(stream)
	if sourceName == "Ani.pm" {
		audioLang = aniPmAudioLanguage(stream)
	}
	subType := ""
	if sourceName == "Ani.pm" || sourceName == "Reanime" {
		// Ani.pm declares burned-in vs toggleable subtitles per row:
		// hard variants carry no subtitle tracks, soft variants do.
		// Tagging SubType keeps same-resolution variants distinct in
		// quality labels and lets ranking prefer hardsubs in anime mode.
		if isHardSubRow(combined) {
			subType = provider.SubTypeHard
		} else {
			subType = provider.SubTypeSoft
		}
	}
	audioLangDisplay := ""
	switch {
	case subType == provider.SubTypeHard && strings.Contains(strings.ToLower(combined), "dub"):
		audioLangDisplay = "Hard Dub"
	case subType == provider.SubTypeHard:
		audioLangDisplay = "Hard Sub"
	case audioLang != "" && audioLang != "en":
		audioLangDisplay = lang.Name(audioLang)
	case strings.Contains(strings.ToLower(stream.Name), "dub") || strings.Contains(strings.ToLower(stream.Description), "dub"):
		audioLangDisplay = "Dub"
	}

	quality := res
	if sourceName != "" {
		if audioLangDisplay != "" {
			quality = fmt.Sprintf("%s [%s] (%s)", res, sourceName, audioLangDisplay)
		} else {
			quality = fmt.Sprintf("%s [%s]", res, sourceName)
		}
	} else if audioLangDisplay != "" {
		quality = fmt.Sprintf("%s (%s)", res, audioLangDisplay)
	}

	// 4. Stream Type: manifests stay HLS; progressive containers
	// (mp4/mkv/webm/…) are progressive. Extensionless URLs consult the
	// Stremio filename hint (already present, no extra request) before
	// falling back to the HLS default for signed manifests.
	streamType := provider.SourceTypeHLS
	lowerURL := strings.ToLower(stream.URL)
	lowerFile := strings.ToLower(stream.BehaviorHints.Filename)
	hasProgressiveExt := func(s string) bool {
		return strings.Contains(s, ".mp4") ||
			strings.Contains(s, ".mkv") ||
			strings.Contains(s, ".webm") ||
			strings.Contains(s, ".avi") ||
			strings.Contains(s, ".mov")
	}
	switch {
	case strings.Contains(lowerURL, ".m3u8"):
		streamType = provider.SourceTypeHLS
	case hasProgressiveExt(lowerURL) || hasProgressiveExt(lowerFile):
		streamType = provider.SourceTypeMP4
	}

	// 5. Headers (Referer, User-Agent, Cookie)
	referer := ""
	userAgent := ""
	cookie := ""

	extractHeaders := func(h map[string]string) {
		for k, v := range h {
			switch strings.ToLower(k) {
			case "referer":
				referer = v
			case "user-agent":
				userAgent = v
			case "cookie":
				cookie = v
			}
		}
	}

	if stream.BehaviorHints.Headers != nil {
		extractHeaders(stream.BehaviorHints.Headers)
	}
	if stream.BehaviorHints.ProxyHeaders != nil && stream.BehaviorHints.ProxyHeaders.Request != nil {
		extractHeaders(stream.BehaviorHints.ProxyHeaders.Request)
	}

	// 6. Subtitles
	subtitles := []provider.SubtitleOption{}
	for _, sub := range stream.Subtitles {
		if sub.URL == "" {
			continue
		}
		subRef := referer
		if subRef == "" && strings.Contains(sub.URL, "pengu.uk") {
			subRef = "https://pengu.uk/"
		}
		subtitles = append(subtitles, provider.SubtitleOption{
			URL:      sub.URL,
			Language: lang.Normalize(sub.Lang),
			Referer:  subRef,
		})
	}

	return provider.MediaSource{
		URL:          stream.URL,
		Quality:      quality,
		Referer:      referer,
		Type:         streamType,
		Subtitles:    subtitles,
		UserAgent:    userAgent,
		CookieHeader: cookie,
		Language:     audioLang,
		SubType:      subType,
	}
}

// isHardSubRow reports whether combined stream metadata carries an
// upstream burned-in subtitle marker ("Hard Sub"/"Hard Dub").
func isHardSubRow(combined string) bool {
	return reHardSubRow.MatchString(combined) || reHardDubRow.MatchString(combined)
}

// aniPmAudioLanguage determines audio language for Ani.pm rows from the
// explicit Audio:/Language: line and filename dub markers only. The row
// description lists subtitle languages ("📝 Subtitles: English, French,
// …") which the shared full-text scan misreads as audio.
func aniPmAudioLanguage(stream penguStreamItem) string {
	if m := reDubSubMarker.FindStringSubmatch(stream.BehaviorHints.Filename); len(m) > 2 {
		if code := mapAudioLanguage(m[1]); code != "" {
			return code
		}
	}
	if m := reAudioLang.FindStringSubmatch(stream.Name + " " + stream.Title + " " + stream.Description); len(m) > 1 {
		if code := mapAudioLanguage(m[1]); code != "" {
			return code
		}
	}
	return ""
}

// streamAudioLanguage determines a stream's audio language as an ISO code,
// "" when original or undetectable. Besides the full-text detection it
// honors MovieBox-style "{lang}dubaudio"/"{lang}subaudio" filename markers,
// which catch codes plain text misses (e.g. "esla", "ptbr").
func streamAudioLanguage(stream penguStreamItem) string {
	combined := stream.Name + " " + stream.Title + " " + stream.Description + " " + stream.BehaviorHints.Filename
	if m := reDubSubMarker.FindStringSubmatch(stream.BehaviorHints.Filename); len(m) > 2 {
		if code := mapAudioLanguage(m[1]); code != "" {
			return code
		}
		if code := mapAudioLanguageFromText(combined); code != "" {
			return code
		}
	}
	return mapAudioLanguageFromText(combined)
}

// canonProviderName returns the canonical display name for a matched provider token.
func canonProviderName(token string) string {
	for _, canon := range []string{
		"4KHDHub", "MovieBox", "VegaMovies", "MoviesDrives",
		"Miruro", "CineFreak", "VAPlayer", "VidLink",
		"VidFast", "Cinejoy", "HDGharTV", "KissKH", "HDHub4u",
		"Ani.pm", "2Peckle", "DebridsCloud", "5Clover", "DaddyLive", "Binged", "StreamedPK", "CDNLIVE", "TimStreams",
		"Arctic", "VixSrc", "Vidy", "ToonWorld4All", "Reanime", "PlayFy",
		"Obejrzyj", "LaMovie", "SoloLatino", "Moflix", "PremiumSmart",
	} {
		if strings.EqualFold(canon, token) {
			return canon
		}
	}
	if strings.EqualFold(token, "anipm") {
		return "Ani.pm"
	}
	if strings.EqualFold(token, "toonworld") {
		return "ToonWorld4All"
	}
	return strings.ToUpper(token)
}

// mapAudioLanguageFromText inspects the combined stream metadata for language tags.
func mapAudioLanguageFromText(combined string) string {
	if m := reAudioLang.FindStringSubmatch(combined); len(m) > 1 {
		if code := mapAudioLanguage(m[1]); code != "" {
			return code
		}
	}
	lower := strings.ToLower(combined)
	for _, l := range audioLanguages {
		if l.Code == "en" {
			continue
		}
		if strings.Contains(lower, strings.ToLower(l.Display)) || strings.Contains(lower, "["+l.Code+"]") {
			return l.Code
		}
	}
	if strings.Contains(lower, "english") {
		return "en"
	}
	return ""
}

// mapAudioLanguage normalizes language text to a standard 2-letter language code.
func mapAudioLanguage(raw string) string {
	for _, candidate := range strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == '/' || r == '|'
	}) {
		if code := lang.Normalize(candidate); isPenguAudioLanguage(code) {
			return code
		}
	}
	return ""
}

func isPenguAudioLanguage(code string) bool {
	for _, language := range audioLanguages {
		if language.Code == code {
			return true
		}
	}
	return false
}
func cleanEmojis(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if isEmoji(r) {
			continue
		}
		b.WriteRune(r)
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

func isEmoji(r rune) bool {
	return (r >= 0x1F000 && r <= 0x1FAFF) ||
		(r >= 0x2600 && r <= 0x27BF) ||
		(r >= 0x2300 && r <= 0x23FF) ||
		(r >= 0x2B50 && r <= 0x2B55) ||
		r == 0xFE0F || r == 0xFE0E || r == 0x200D
}
