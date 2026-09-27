package moovie

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
	"kari/internal/provider/kit"
)

const (
	requestTimeoutMS = 10000
	softTimeoutMS    = 8000
	sourceGraceMS    = 1500
	sourceQuorum     = 1
	providerCacheMS  = 5000
)

// --- Upstream types (private wire DTOs) ---

type providerListResp map[string]struct {
	Name         string `json:"name"`
	Enabled      bool   `json:"enabled"`
	IsClientSide bool   `json:"isClientSide"`
	Priority     int    `json:"priority"`
}

type sseItem struct {
	Name      string            `json:"name"`
	Title     string            `json:"title"`
	URL       string            `json:"url"`
	Playlist  string            `json:"playlist"`
	ProxyURL  string            `json:"proxyUrl"`
	Headers   map[string]string `json:"headers"`
	Quality   string            `json:"quality"`
	Type      string            `json:"type"`
	Qualities map[string]struct {
		URL      string `json:"url"`
		Playlist string `json:"playlist"`
		Type     string `json:"type"`
	} `json:"qualities"`
	LanguageVariants []struct {
		Language   string            `json:"language"`
		Label      string            `json:"label"`
		URL        string            `json:"url"`
		ProxyURL   string            `json:"proxyUrl"`
		Headers    map[string]string `json:"headers"`
		Type       string            `json:"type"`
		StreamType string            `json:"streamType"`
		Provider   string            `json:"provider"`
		CatalogID  string            `json:"catalogId"`
		ID         string            `json:"id"`
	} `json:"_languageVariants"`
	Captions []struct {
		URL      string `json:"url"`
		File     string `json:"file"`
		Language string `json:"language"`
		Lang     string `json:"lang"`
		Name     string `json:"name"`
	} `json:"captions"`
}

type searchResp struct {
	Results []struct {
		Provider     string    `json:"provider"`
		ProviderName string    `json:"providerName"`
		Priority     int       `json:"priority"`
		Streams      []sseItem `json:"streams"`
	} `json:"results"`
}

type subtitleItem struct {
	Language   string `json:"language"`
	URL        string `json:"url"`
	NeedsProxy bool   `json:"needsProxy"`
}

type resolveVariantResp struct {
	URL      string            `json:"url"`
	ProxyURL string            `json:"proxyUrl"`
	Type     string            `json:"type"`
	Headers  map[string]string `json:"headers"`
}

// --- Merged results ---

type mergedSource struct {
	url     string
	quality string
	typ     string
	referer string
}

type mergedAudio struct {
	url     string
	lang    string
	typ     string
	referer string
}

type mergedSubtitle struct {
	url     string
	lang    string
	referer string
}

type mergedResults struct {
	sources   []mergedSource
	audio     []mergedAudio
	subtitles []mergedSubtitle
}

// --- Provider list ---

func (c *Client) fetchProviders(ctx context.Context) ([]upstreamProvider, error) {
	c.mu.Lock()
	if c.providerCache != nil && time.Now().UnixMilli() < c.providerExp {
		cached := c.providerCache
		c.mu.Unlock()
		return cached, nil
	}
	c.mu.Unlock()

	target := fmt.Sprintf("%s/api/providers?_cb=%d", c.apiBase, time.Now().UnixMilli())
	var body providerListResp
	if err := getJSON(ctx, c.httpClient, target, &body); err != nil {
		return nil, fmt.Errorf("moovie providers: %w", err)
	}
	var out []upstreamProvider
	for id, p := range body {
		if !p.Enabled || p.IsClientSide || strings.TrimSpace(p.Name) == "" {
			continue
		}
		out = append(out, upstreamProvider{id: id, name: p.Name, priority: p.Priority})
	}
	sortProviders(out)

	c.mu.Lock()
	c.providerCache = out
	c.providerExp = time.Now().UnixMilli() + providerCacheMS
	c.mu.Unlock()
	return out, nil
}

func sortProviders(providers []upstreamProvider) {
	for i := 1; i < len(providers); i++ {
		for j := i; j > 0 && providers[j].priority < providers[j-1].priority; j-- {
			providers[j], providers[j-1] = providers[j-1], providers[j]
		}
	}
}

// --- SSE scrape ---

type sseEvent struct {
	event string
	data  string
}

func parseSSE(text string) []sseEvent {
	var events []sseEvent
	for _, block := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n\n") {
		event := "message"
		var data []string
		for _, line := range strings.Split(block, "\n") {
			switch {
			case strings.HasPrefix(line, ":"):
				continue
			case strings.HasPrefix(line, "event:"):
				event = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
			case strings.HasPrefix(line, "data:"):
				data = append(data, strings.TrimLeft(strings.TrimPrefix(line, "data:"), " "))
			case len(data) > 0:
				// Multi-line data blocks join with \n per the SSE spec.
				data = append(data, line)
			}
		}
		if len(data) == 0 {
			continue
		}
		events = append(events, sseEvent{event: event, data: strings.Join(data, "\n")})
	}
	return events
}

type scrapeResult struct {
	items    []sseItem
	variants []variantEntry
	captions []captionEntry
	provider upstreamProvider
	err      error
}

// variantEntry is one dubbed-audio variant needing a direct URL or an API
// resolution round-trip. resolveProvider/resolveID address the variant API,
// splitting the "provider:id" form when present.
type variantEntry struct {
	language        string
	label           string
	url             string
	proxyURL        string
	headers         map[string]string
	typeHint        string
	provider        upstreamProvider
	resolveProvider string
	resolveID       string
}

// captionEntry is one subtitle track riding on a stream item.
type captionEntry struct {
	url      string
	language string
	name     string
	referer  string
}

// fetchProviderStreams scrapes one provider over SSE, collecting completed
// stream items, audio variants, and captions.
func (c *Client) fetchProviderStreams(ctx context.Context, p upstreamProvider, tmdbID int, mediaType string, season, episode int) scrapeResult {
	params := url.Values{}
	params.Set("id", p.id)
	params.Set("tmdbId", strconv.Itoa(tmdbID))
	params.Set("type", mediaType)
	params.Set("starred", "1")
	params.Set("fallback", "false")
	params.Set("_cb", strconv.FormatInt(time.Now().UnixMilli(), 10))
	if mediaType == "tv" {
		params.Set("season", strconv.Itoa(season))
		params.Set("episode", strconv.Itoa(episode))
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.apiBase+"/scrape/source?"+params.Encode(), nil)
	if err != nil {
		return scrapeResult{provider: p, err: fmt.Errorf("build scrape request: %w", err)}
	}
	req.Header.Set("User-Agent", config.DesktopUserAgent)
	req.Header.Set("Accept", "text/event-stream")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return scrapeResult{provider: p, err: fmt.Errorf("scrape request: %w", err)}
	}

	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return scrapeResult{provider: p, err: &provider.HTTPError{Code: resp.StatusCode, URL: c.apiBase + "/scrape/source"}}
	}
	var out scrapeResult
	out.provider = p
	var parseErr error
	readErr := readSSE(resp.Body, func(ev sseEvent) {
		if ev.event != "completed" {
			return
		}
		if err := appendCompletedEvent(&out, ev.data, p); err != nil && parseErr == nil {
			parseErr = err
		}
	})
	if readErr != nil && len(out.items) == 0 {
		out.err = fmt.Errorf("read scrape response: %w", readErr)
	}
	if len(out.items) == 0 && parseErr != nil {
		out.err = parseErr
	}
	return out
}

func appendCompletedEvent(out *scrapeResult, data string, p upstreamProvider) error {
	var payload struct {
		Streams json.RawMessage `json:"streams"`
		Stream  json.RawMessage `json:"stream"`
		Embeds  json.RawMessage `json:"embeds"`
	}
	if err := json.Unmarshal([]byte(data), &payload); err != nil {
		return fmt.Errorf("decode completed event: %w", err)
	}
	items := decodeStreamList(payload.Streams, payload.Stream)
	if len(items) == 0 {
		items = decodeStreamList(nil, payload.Embeds)
	}
	for _, item := range items {
		out.items = append(out.items, item)
		for _, v := range item.LanguageVariants {
			rp, rid := v.Provider, v.CatalogID
			if rid == "" {
				rid = v.ID
			}
			if i := strings.Index(rid, ":"); i >= 0 {
				if head := rid[:i]; head != "" {
					rp = head
				}
				rid = rid[i+1:]
			}
			out.variants = append(out.variants, variantEntry{
				language: v.Language, label: v.Label, url: v.URL,
				proxyURL: v.ProxyURL, headers: v.Headers, typeHint: v.Type,
				provider: p, resolveProvider: rp, resolveID: rid,
			})
		}
		for _, cp := range item.Captions {
			captionURL := strings.TrimSpace(cp.URL)
			if captionURL == "" {
				captionURL = strings.TrimSpace(cp.File)
			}
			out.captions = append(out.captions, captionEntry{
				url:      captionURL,
				language: cp.Language,
				name:     cp.Name,
				referer:  refererOf(item.Headers),
			})
		}
	}
	return nil
}

func readSSE(body io.Reader, consume func(sseEvent)) error {
	reader := bufio.NewReaderSize(body, 32*1024)
	var event string
	var data []string
	var total int64
	dispatch := func() bool {
		if event == "done" {
			return true
		}
		if len(data) == 0 {
			event = ""
			return false
		}
		consume(sseEvent{event: event, data: strings.Join(data, "\n")})
		done := event == "done"
		event = ""
		data = nil
		return done
	}
	for {
		line, err := reader.ReadString('\n')
		if len(line) > 0 {
			total += int64(len(line))
			if total > httpclient.MaxBodyBytes {
				return httpclient.ErrBodyTooLarge
			}
			line = strings.TrimSuffix(line, "\n")
			line = strings.TrimSuffix(line, "\r")
			switch {
			case line == "":
				if dispatch() {
					return nil
				}
			case strings.HasPrefix(line, ":"):
			case strings.HasPrefix(line, "event:"):
				event = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
			case strings.HasPrefix(line, "data:"):
				data = append(data, strings.TrimLeft(strings.TrimPrefix(line, "data:"), " "))
			case len(data) > 0:
				data = append(data, line)
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				dispatch()
				return nil
			}
			return err
		}
	}
}

// decodeStreamList handles the stream/streams/embed payload shapes.
func decodeStreamList(streams, stream json.RawMessage) []sseItem {
	if len(streams) > 0 {
		var list []sseItem
		if err := json.Unmarshal(streams, &list); err == nil {
			return list
		}
	}
	if len(stream) > 0 {
		var list []sseItem
		if err := json.Unmarshal(stream, &list); err == nil {
			return list
		}
		var single sseItem
		if err := json.Unmarshal(stream, &single); err == nil && (single.URL != "" || single.Playlist != "") {
			return []sseItem{single}
		}
	}
	return nil
}

// --- Fan-out with quorum/grace timing (mirrors movy's cadence) ---

func (c *Client) collectCandidates(ctx context.Context, providers []upstreamProvider, tmdbID int, mediaType string, season, episode int) []scrapeResult {
	if len(providers) == 0 {
		return nil
	}
	results := make(chan scrapeResult, len(providers))
	var wg sync.WaitGroup
	for _, p := range providers {
		wg.Add(1)
		go func(p upstreamProvider) {
			defer wg.Done()
			reqCtx, cancel := context.WithTimeout(ctx, requestTimeoutMS*time.Millisecond)
			defer cancel()
			results <- c.fetchProviderStreams(reqCtx, p, tmdbID, mediaType, season, episode)
		}(p)
	}
	go func() {
		wg.Wait()
		close(results)
	}()

	var collected []scrapeResult
	withStreams := func() int {
		n := 0
		for _, r := range collected {
			if len(r.items) > 0 {
				n++
			}
		}
		return n
	}

	softTimer := time.NewTimer(softTimeoutMS * time.Millisecond)
	defer softTimer.Stop()
	var graceTimer <-chan time.Time
	graceStarted := false

	for results != nil {
		select {
		case r, ok := <-results:
			if !ok {
				results = nil
				break
			}
			collected = append(collected, r)
			if !graceStarted && withStreams() >= sourceQuorum {
				graceStarted = true
				graceTimer = time.After(sourceGraceMS * time.Millisecond)
			}
		case <-graceTimer:
			return collected
		case <-softTimer.C:
			if withStreams() > 0 {
				return collected
			}
			softTimer.Reset(softTimeoutMS * time.Millisecond)
		case <-ctx.Done():
			return collected
		}
	}
	return collected
}

// fetchSourcesDirect runs provider discovery, subtitle loading, scraping,
// fallback, variant resolution, and merge for one TMDB item.
func (c *Client) fetchSourcesDirect(ctx context.Context, mediaType string, tmdbID, season, episode int) (*mergedResults, error) {
	providers, err := c.fetchProviders(ctx)
	if err != nil {
		return nil, err
	}

	var subtitleCaptions []subtitleItem
	var subWG sync.WaitGroup
	subWG.Add(1)
	go func() {
		defer subWG.Done()
		subtitleCaptions = c.fetchSubtitles(ctx, tmdbID, mediaType, season, episode)
	}()

	collected := c.collectCandidates(ctx, providers, tmdbID, mediaType, season, episode)

	hasStreams := false
	var scrapeErr error
	for _, r := range collected {
		if len(r.items) > 0 {
			hasStreams = true
		}
		if r.err != nil && scrapeErr == nil {
			scrapeErr = r.err
		}
	}
	var fallbackErr error
	if !hasStreams {
		fb, err := c.fetchSearchFallback(ctx, tmdbID, mediaType, season, episode)
		if err == nil && len(fb.items) > 0 {
			collected = []scrapeResult{fb}
			hasStreams = true
		} else if err != nil {
			fallbackErr = err
		}
	}
	subWG.Wait()

	if !hasStreams {
		if scrapeErr != nil {
			return nil, fmt.Errorf("moovie sources: %w", scrapeErr)
		}
		if fallbackErr != nil {
			return nil, fmt.Errorf("moovie fallback: %w", fallbackErr)
		}
		return nil, provider.ErrNoSources
	}
	return c.merge(ctx, collected, subtitleCaptions, mediaType, season, episode), nil
}

// --- Mapping (mirrors the web player's Playback rules) ---

// playableEntry flattens one raw item into candidate rows: qualities-map
// entries first, else a single row. Header-bearing URLs play through
// kari's native mpv headers (no proxy); proxyUrl rows play as-is.
func playableEntry(item sseItem, p upstreamProvider) []candidateRow {
	var out []candidateRow
	push := func(rawURL, proxyURL string, headers map[string]string, quality, typeHint string) {
		u := strings.TrimSpace(proxyURL)
		if u == "" {
			u = strings.TrimSpace(rawURL)
		}
		if u == "" {
			return
		}
		out = append(out, candidateRow{
			url: u, quality: quality, typeHint: typeHint, provider: p,
			headers: headers,
		})
	}
	isHLS := item.Type == "hls" || strings.TrimSpace(item.Playlist) != ""
	if len(item.Qualities) > 0 {
		for label, q := range item.Qualities {
			rawURL := q.Playlist
			if !isHLS {
				rawURL = q.URL
				if rawURL == "" {
					rawURL = q.Playlist
				}
			} else if rawURL == "" {
				rawURL = q.URL
			}
			push(rawURL, item.ProxyURL, item.Headers, label, q.Type)
		}
		return out
	}
	rawURL := item.Playlist
	if !isHLS {
		rawURL = item.URL
		if rawURL == "" {
			rawURL = item.Playlist
		}
	} else if rawURL == "" {
		rawURL = item.URL
	}
	push(rawURL, item.ProxyURL, item.Headers, item.Quality, item.Type)
	return out
}

type candidateRow struct {
	url       string
	quality   string
	typeHint  string
	provider  upstreamProvider
	headers   map[string]string
	audioLang string
}

// merge flattens candidates, resolves audio variants, enriches HLS quality
// from master manifests, and merges everything best-first.
func (c *Client) merge(ctx context.Context, collected []scrapeResult, subtitleCaptions []subtitleItem, mediaType string, season, episode int) *mergedResults {
	seenURLs := make(map[string]struct{})
	var flats []candidateRow
	for _, r := range collected {
		for _, item := range r.items {
			for _, row := range playableEntry(item, r.provider) {
				if _, ok := seenURLs[row.url]; ok {
					continue
				}
				seenURLs[row.url] = struct{}{}
				flats = append(flats, row)
			}
		}
	}
	if len(flats) == 0 {
		return &mergedResults{}
	}

	// Audio variants: direct URLs pass through with their headers;
	// entries without one resolve via the variant API.
	var variantMu sync.Mutex
	var audioRows []candidateRow
	var variantWG sync.WaitGroup
	for _, r := range collected {
		for _, v := range r.variants {
			variantWG.Add(1)
			go func(v variantEntry) {
				defer variantWG.Done()
				row, ok := c.resolveAudioVariant(ctx, v, mediaType, season, episode)
				if !ok {
					return
				}
				variantMu.Lock()
				audioRows = append(audioRows, row)
				variantMu.Unlock()
			}(v)
		}
	}
	variantWG.Wait()

	// Dub-labeled source rows are audio tracks, not qualities.
	var sourceRows []candidateRow
	for _, row := range flats {
		if dl := kit.DubLangOf(row.quality); dl != "" {
			row.audioLang = dl
			audioRows = append(audioRows, row)
			continue
		}
		sourceRows = append(sourceRows, row)
	}

	// Enrich HLS rows with manifest-measured quality, five at a time so
	// one slow CDN never gates the rest.
	var enrichWG sync.WaitGroup
	enrichSem := make(chan struct{}, 5)
	for i := range sourceRows {
		if streamType(sourceRows[i].url, sourceRows[i].typeHint) != provider.SourceTypeHLS {
			continue
		}
		enrichWG.Add(1)
		go func(i int) {
			defer enrichWG.Done()
			enrichSem <- struct{}{}
			defer func() { <-enrichSem }()
			if label := kit.DetectHLSQuality(ctx, c.httpClient, sourceRows[i].url, sourceRows[i].headers); label != "auto" {
				sourceRows[i].quality = label
			}
		}(i)
	}
	enrichWG.Wait()
	sortCandidates(sourceRows)
	out := &mergedResults{}

	for _, row := range sourceRows {
		typ := streamType(row.url, row.typeHint)
		out.sources = append(out.sources, mergedSource{
			url:     row.url,
			quality: normalizeQuality(row.quality),
			typ:     typ,
			referer: refererOf(row.headers),
		})
	}
	for _, row := range audioRows {
		typ := streamType(row.url, row.typeHint)
		language := row.audioLang
		if language == "" {
			language = "und"
		}
		out.audio = append(out.audio, mergedAudio{
			url: row.url, lang: language, typ: typ,
			referer: refererOf(row.headers),
		})
	}

	// Subtitles: direct API captions plus per-item captions; proxy-wrapped
	// entries are skipped (kari hosts no proxy).
	seenSubs := make(map[string]struct{})
	addSub := func(rawURL, language, referer string) {
		u := strings.TrimSpace(rawURL)
		if u == "" {
			return
		}
		if !kit.IsSubtitleURL(u) {
			return
		}
		key := u + "\x00" + strings.TrimSpace(referer)
		if _, ok := seenSubs[key]; ok {
			return
		}
		seenSubs[key] = struct{}{}
		language = lang.Normalize(language)
		if language == "" {
			language = "und"
		}
		out.subtitles = append(out.subtitles, mergedSubtitle{
			url:     u,
			lang:    language,
			referer: referer,
		})
	}
	for _, caption := range subtitleCaptions {
		if caption.NeedsProxy {
			continue
		}
		addSub(caption.URL, caption.Language, "")
	}
	for _, result := range collected {
		for _, caption := range result.captions {
			addSub(caption.url, caption.language, caption.referer)
		}
	}
	return out
}

// resolveAudioVariant turns one audio variant into a playable row: direct
// URLs pass through, others resolve via the variant API. Headers travel
// with the row for kari's native mpv header injection.
func (c *Client) resolveAudioVariant(ctx context.Context, v variantEntry, mediaType string, season, episode int) (candidateRow, bool) {
	language := lang.Normalize(strings.TrimSpace(v.language))
	if language == "" {
		language = lang.Normalize(strings.TrimSpace(v.label))
	}
	mkRow := func(rawURL, proxyURL string, headers map[string]string, typeHint string) (candidateRow, bool) {
		u := strings.TrimSpace(proxyURL)
		if u == "" {
			u = strings.TrimSpace(rawURL)
		}
		if u == "" {
			return candidateRow{}, false
		}
		if language == "" {
			language = "und"
		}
		return candidateRow{url: u, quality: "auto", typeHint: typeHint, provider: v.provider, headers: headers, audioLang: language}, true
	}
	// A variant carrying any URL (direct or proxy) needs no API round-trip;
	// mkRow already prefers the proxy address.
	if row, ok := mkRow(v.url, v.proxyURL, v.headers, v.typeHint); ok {
		return row, true
	}
	if strings.TrimSpace(v.resolveID) == "" {
		return candidateRow{}, false
	}
	params := url.Values{}
	params.Set("provider", v.resolveProvider)
	params.Set("id", v.resolveID)
	params.Set("type", mediaType)
	if mediaType == "tv" {
		params.Set("season", strconv.Itoa(season))
		params.Set("episode", strconv.Itoa(episode))
	}
	var out resolveVariantResp
	if err := getJSON(ctx, c.httpClient, c.apiBase+"/api/resolve-variant?"+params.Encode(), &out); err != nil {
		return candidateRow{}, false
	}
	u := strings.TrimSpace(out.ProxyURL)
	if u == "" {
		u = strings.TrimSpace(out.URL)
	}
	if u == "" {
		return candidateRow{}, false
	}
	return mkRow(u, "", out.Headers, out.Type)
}

// fetchSearchFallback queries the REST search endpoint the web player uses
// when SSE yields nothing.
func (c *Client) fetchSearchFallback(ctx context.Context, tmdbID int, mediaType string, season, episode int) (scrapeResult, error) {
	params := url.Values{}
	params.Set("q", strconv.Itoa(tmdbID))
	params.Set("type", mediaType)
	params.Set("starred", "1")
	params.Set("_cb", strconv.FormatInt(time.Now().UnixMilli(), 10))
	if mediaType == "tv" {
		params.Set("season", strconv.Itoa(season))
		params.Set("episode", strconv.Itoa(episode))
	}
	var body searchResp
	if err := getJSON(ctx, c.httpClient, c.apiBase+"/api/search?"+params.Encode(), &body); err != nil {
		return scrapeResult{}, fmt.Errorf("moovie search fallback: %w", err)
	}
	var out scrapeResult
	out.provider = upstreamProvider{id: "search", name: "search", priority: 999}
	for _, result := range body.Results {
		out.items = append(out.items, result.Streams...)
	}
	return out, nil
}

// fetchSubtitles loads caption tracks; proxy-wrapped entries are marked for
// skipping at merge time.
func (c *Client) fetchSubtitles(ctx context.Context, tmdbID int, mediaType string, season, episode int) []subtitleItem {
	params := url.Values{}
	params.Set("tmdbId", strconv.Itoa(tmdbID))
	params.Set("type", mediaType)
	params.Set("_cb", strconv.FormatInt(time.Now().UnixMilli(), 10))
	if mediaType == "tv" {
		params.Set("season", strconv.Itoa(season))
		params.Set("episode", strconv.Itoa(episode))
	}
	var body struct {
		Captions []subtitleItem `json:"captions"`
	}
	if err := getJSON(ctx, c.httpClient, c.apiBase+"/api/subtitles?"+params.Encode(), &body); err != nil {
		logging.Debug("moovie subtitles failed", "err", err)
		return nil
	}
	return body.Captions
}

// --- Shared helpers (ported from the reference implementation) ---

var qualityRanks = map[string]int{
	"2160p": 0, "4k": 0, "1080p": 1, "720p": 2, "480p": 3, "360p": 4, "auto": 5,
}

// normalizeQuality folds upstream quality labels to the canonical set.
func normalizeQuality(quality string) string {
	switch key := strings.ToLower(strings.TrimSpace(quality)); key {
	case "2160p", "4k", "uhd":
		return "2160p"
	case "1080p", "fhd", "fullhd", "full-hd":
		return "1080p"
	case "720p", "hd":
		return "720p"
	case "480p", "sd":
		return "480p"
	case "360p":
		return "360p"
	default:
		return "auto"
	}
}

func qualityRank(quality string) int {
	if r, ok := qualityRanks[quality]; ok {
		return r
	}
	return 99
}

// sortCandidates orders rows best-first: quality, provider priority, then
// progressive mp4 over manifests at parity.
func sortCandidates(rows []candidateRow) {
	for i := 1; i < len(rows); i++ {
		for j := i; j > 0; j-- {
			a, b := rows[j], rows[j-1]
			swap := false
			if ra, rb := qualityRank(a.quality), qualityRank(b.quality); ra != rb {
				swap = ra < rb
			} else if a.provider.priority != b.provider.priority {
				swap = a.provider.priority < b.provider.priority
			} else {
				swap = kit.IsMp4URL(a.url) && !kit.IsMp4URL(b.url)
			}
			if !swap {
				break
			}
			rows[j], rows[j-1] = b, a
		}
	}
}

// streamType classifies a stream for the player layer, preferring the
// declared upstream type and inferring from the URL otherwise.
func streamType(rawURL, declared string) string {
	switch strings.ToLower(strings.TrimSpace(declared)) {
	case provider.SourceTypeHLS, provider.SourceTypeM3U8:
		return provider.SourceTypeHLS
	case provider.SourceTypeMP4:
		return provider.SourceTypeMP4
	case "dash":
		return "dash"
	}
	lowerURL := strings.ToLower(rawURL)
	switch {
	case strings.Contains(lowerURL, ".m3u8"):
		return provider.SourceTypeHLS
	case strings.Contains(lowerURL, ".mpd"):
		return "dash"
	case strings.Contains(lowerURL, ".mp4") ||
		strings.Contains(lowerURL, ".mkv") ||
		strings.Contains(lowerURL, ".webm") ||
		strings.Contains(lowerURL, ".avi") ||
		strings.Contains(lowerURL, ".mov"):
		return provider.SourceTypeMP4
	case strings.TrimSpace(declared) != "":
		return strings.ToLower(strings.TrimSpace(declared))
	default:
		return provider.SourceTypeMP4
	}
}

// refererOf extracts the player Referer from per-stream headers.
func refererOf(headers map[string]string) string {
	for k, v := range headers {
		if strings.EqualFold(k, "referer") {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func getJSON(ctx context.Context, hc *http.Client, target string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", config.DesktopUserAgent)
	resp, err := hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return &provider.HTTPError{Code: resp.StatusCode, URL: target}
	}
	body, err := httpclient.ReadCapped(resp)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	return nil
}
