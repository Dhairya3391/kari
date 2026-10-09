// Package pengu implements provider.Provider against the PenguPlay aggregator
// API (https://pengu.uk). PenguPlay is an aggregator that queries multiple
// streaming backends (4KHDHub, MovieBox, VegaMovies, VAPlayer, Miruro,
// VidFast, CineFreak, Cinejoy, KissKH, HDHub4u, Anikoto, 2Peckle, Arctic,
// Atlantic, VixSrc) and returns direct and HLS streams. VidLink and
// MoviesDrives stay disabled because live mpv probes show VidLink's upstream
// answering 428/429 and MoviesDrives returning no playable streams, and
// Movy.sx is omitted because Kari handles it independently. Atlantic and
// VixSrc carry no config key upstream (always-on backends); Arctic does and
// is enabled below so its streams flow automatically once upstream recovers.
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
	"strconv"
	"strings"
	"time"

	"kari/internal/config"
	"kari/internal/httpclient"
	"kari/internal/lang"
	"kari/internal/logging"
	"kari/internal/provider"
	"kari/internal/provider/streambase"
	"kari/internal/tmdb"
)

// log scopes every line from this package with its identity.
var pgLog = logging.With("provider", "pengu")

var penguAPIBase = config.PenguAPIBase

const penguUA = config.DesktopUserAgent

var (
	reResolution = regexp.MustCompile(`(?i)\b(4k|2160p|1440p|1080p|720p|480p|360p)\b`)
	reSourceTag  = regexp.MustCompile(`(?i)\b(4khdhub|moviebox|vegamovies|moviesdrives|miruro|cinefreak|vaplayer|vidlink|vidfast|cinejoy|hdghartv|kisskh|hdhub4u|atlantic|anikoto|2peckle|debridscloud|5clover|daddylive|binged|streamedpk|cdnlive|timstreams|arctic|vixsrc)\b`)
	reAudioLang  = regexp.MustCompile(`(?i)(?:audio|language|languages):\s*([a-zA-Z, /]+)`)
	// reDubSubMarker matches MovieBox-style audio markers in filenames:
	// "{lang}dubaudio" (dubbed audio) vs "{lang}subaudio" (original audio
	// with subtitles) vs "OriginalAudio".
	reDubSubMarker  = regexp.MustCompile(`(?i)([a-z]+)(dub|sub)audio`)
	reQualityHeight = regexp.MustCompile(`(?i)\b(4k|2160|1080|720|480|360)p?\b`)
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
	// langFilter mirrors the user's audio-language settings (see
	// internal/settings LanguageFilter): nil or missing keys mean enabled.
	// It filters foreign dubbed audio at resolve time; original-audio and
	// English rows always pass.
	langFilter map[string]bool
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
	return &Client{
		base:          base,
		httpClient:    httpclient.NewWithUserAgent(penguUA),
		authToken:     token,
		configSegment: segment,
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
		{Name: provider.ModeLive, Priority: 1},
	}
}

// Features implements provider.FeatureSource.
func (c *Client) Features(mode provider.ContentType) provider.Features {
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
}

// AudioLanguages implements provider.AudioLanguagesSource.
func (c *Client) AudioLanguages() []provider.AudioLanguage {
	return audioLanguages
}

// Search delegates to TMDB search base for Movies/TV/Cartoons, and queries
// PenguPlay's live sports catalogs when in ModeLive.
func (c *Client) Search(ctx context.Context, query string, mode provider.ContentType) ([]provider.SearchResult, error) {
	if mode == provider.ModeLive {
		return c.searchLive(ctx, query)
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
	return c.base.FetchEpisodes(ctx, series)
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

	return c.resolveStreams(ctx, mediaType, stremioID, tmdbID)
}

func (c *Client) resolveLiveSource(ctx context.Context, stremioID string) ([]provider.MediaSource, error) {
	resp, err := c.fetchPenguStreams(ctx, provider.MediaTypeTV, stremioID)
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

func (c *Client) resolveStreams(ctx context.Context, mediaType, stremioID string, tmdbID int) ([]provider.MediaSource, error) {
	resp, err := c.fetchPenguStreams(ctx, mediaType, stremioID)
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
		if trackLang := streamAudioLanguage(stream); trackLang != "" && trackLang != "en" && !c.languageEnabled(trackLang, lang.Name(trackLang)) {
			dropped = append(dropped, resolveCandidate{source: source, stream: stream})
			continue
		}
		kept = append(kept, resolveCandidate{source: source, stream: stream})
	}

	candidates := kept
	if len(candidates) == 0 && len(dropped) > 0 {
		// Everything was disabled foreign audio: serve it anyway rather
		// than failing the title outright.
		candidates = dropped
	}
	sources := collapseMirrors(candidates)
	if len(sources) == 0 {
		return nil, provider.ErrNoSources
	}
	return sources, nil
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
	type mirrorKey struct {
		backend   string
		height    int
		lang      string
		container string
		size      int64
	}
	bestIdx := make(map[mirrorKey]int)
	sources := make([]provider.MediaSource, 0, len(candidates))
	for _, cand := range candidates {
		key := mirrorKey{
			backend:   mirrorBackend(cand.source.Quality),
			height:    qualityHeight(cand.source.Quality),
			lang:      cand.source.Language,
			container: streamContainer(cand.source),
			size:      cand.stream.BehaviorHints.VideoSize,
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

// isExcludedStream checks if a stream originated from an excluded backend
// (Movy.sx is maintained independently in Kari).
func isExcludedStream(stream penguStreamItem) bool {
	combined := strings.ToLower(stream.Name + " " + stream.Title + " " + stream.Description + " " + stream.URL)
	return strings.Contains(combined, "movysx") || strings.Contains(combined, "movy.sx")
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

// buildConfigSegment compiles the functional multi-provider configuration into a
// compact raw-deflated base64url segment expected by PenguPlay.
// Movy.sx is deliberately omitted because Kari manages Movy.sx independently.
// The key set tracks the upstream manifest (v1.4.0, re-checked 2026-09-21):
// Arctic is enabled even though upstream status reports it DOWN — a dead
// source costs nothing (verified: enabling it changes nothing in live
// responses) and its streams flow automatically once upstream recovers.
// source_111477 stays off (probed: yields no playable streams) and
// source_live-vault stays off (unverified for Kari's live path). Every other
// backend below was verified with live mpv playback probes on 2026-09-20:
// MovieBox, Miruro, Anikoto and 2Peckle play with Kari's header handling so
// they stay enabled, while VidLink (upstream answers 428/429 even on fresh
// URLs) and MoviesDrives (zero playable streams across movies and series)
// stay off.
func buildConfigSegment(token string) (string, error) {
	cfg := map[string]any{
		"source_4khdhub":      "on",
		"source_moviebox":     "on",
		"source_vegamovies":   "on",
		"source_moviesdrives": "off",
		"source_vaplayer":     "on",
		"source_miruro":       "on",
		"source_vidlink":      "off",
		"source_vidfast":      "on",
		"source_cinefreak":    "on",
		"source_cinejoy":      "on",
		"source_kisskh":       "on",
		"source_hdhub4u":      "on",
		"source_anikoto":      "on",
		"source_2peckle":      "on",
		"source_arctic":       "off",
		"source_live-sports":  "on",
		"res_2160":            "on",
		"res_1080":            "on",
		"res_720":             "on",
		"res_480":             "on",
		"res_360":             "on",
	}
	if token != "" {
		cfg["auth_token"] = token
	}

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
func (c *Client) fetchPenguStreams(ctx context.Context, mediaType, stremioID string) (*penguResponse, error) {
	pgLog.Debug("fetch start", "mediaType", mediaType, "id", stremioID)

	stremioType := "movie"
	if strings.HasPrefix(stremioID, "pp-live:") || mediaType == provider.MediaTypeLive {
		stremioType = "tv"
	} else if mediaType == provider.MediaTypeTV {
		stremioType = "series"
	}
	url := fmt.Sprintf("%s/%s/stream/%s/%s.json", penguAPIBase, c.configSegment, stremioType, stremioID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("pengu request: %w", err)
	}
	req.Header.Set("User-Agent", penguUA)
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("pengu do request: %w", redactRequestError(err, c.configSegment))
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
	// misses (e.g. "esla", "ptbr").
	audioLang := streamAudioLanguage(stream)
	audioLangDisplay := ""
	if audioLang != "" && audioLang != "en" {
		audioLangDisplay = lang.Name(audioLang)
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
	userAgent := penguUA
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
		subtitles = append(subtitles, provider.SubtitleOption{
			URL:      sub.URL,
			Language: lang.Normalize(sub.Lang),
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
	}
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
		"VidFast", "Cinejoy", "HDGharTV", "KissKH", "HDHub4u", "Atlantic",
		"Anikoto", "2Peckle", "DebridsCloud", "5Clover", "DaddyLive", "Binged", "StreamedPK", "CDNLIVE", "TimStreams",
		"Arctic", "VixSrc",
	} {
		if strings.EqualFold(canon, token) {
			return canon
		}
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
