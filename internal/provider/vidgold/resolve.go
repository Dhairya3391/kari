package vidgold

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"

	"kari/internal/config"
	"kari/internal/lang"
	"kari/internal/logging"
	"kari/internal/provider"
	"kari/internal/provider/kit"
	"kari/internal/timeouts"
)

const (
	// minDeclaredHeight is the smallest h value treated as a pixel height.
	// Some lanes send h=1 as a flag, which must not become a "1p" quality.
	minDeclaredHeight = 240
	// maxAPIBytes caps one lane's JSON response. The API embeds a full media
	// playlist for some lanes, so responses can be large but never unbounded.
	maxAPIBytes = 8 << 20
	// maxPlaylistBytes caps master probing. Every video variant lives near the
	// top of a master, and a media playlist needs only enough lines to prove it
	// has no stream variants — this keeps 4K detection cheap on metered links.
	maxPlaylistBytes = 64 << 10
)

var (
	reResolution        = regexp.MustCompile(`(?i)\bRESOLUTION=(\d+)x(\d+)\b`)
	reBandwidth         = regexp.MustCompile(`(?i)\bBANDWIDTH=(\d+)\b`)
	reSubtitleLabelTrim = regexp.MustCompile(`(?i)\s*(?:hi\d*|\d+)$`)
)

// server is one VidGold stream lane. key is the API's sources parameter and
// label is the name VidGold's player shows for that lane.
type server struct {
	key   string
	label string
}

// servers lists every lane the VidGold player advertises, in the order the
// player presents them. A lane returning no stream is skipped without failing
// the resolution. s1 and s3 can report the same upstream label, so the
// player's own names keep the two lanes distinguishable when it is absent.
var servers = []server{
	{key: "s1", label: "Aurum"},
	{key: "s4", label: "Sovereign"},
	{key: "s2", label: "Bullion"},
	{key: "s3", label: "Ingot"},
	{key: "s5", label: "Nugget"},
	{key: "s7", label: "Doubloon"},
	{key: "s6", label: "Gilt"},
}

type apiResponse struct {
	Meta      json.RawMessage `json:"meta"`
	Source    *apiSource      `json:"source"`
	Subtitles []apiSubtitle   `json:"subtitles"`
}

// apiMeta is the media metadata VidGold embeds in each lane response. Only
// spoken languages carry typed audio identity; the rest is display-only.
type apiMeta struct {
	SpokenLanguages []struct {
		ISO6391 string `json:"iso_639_1"`
		Name    string `json:"name"`
	} `json:"spoken_languages"`
}

type apiSource struct {
	Source    string       `json:"source"`
	Label     string       `json:"label"`
	URL       string       `json:"url"`
	Qualities []apiQuality `json:"qualities"`
	Height    int          `json:"h"`
}

type apiQuality struct {
	Quality string `json:"quality"`
	URL     string `json:"url"`
	Type    string `json:"type"`
}

// apiSubtitle carries only the fields playback needs; the API's type/source
// keys and the inline manifests some lanes return are intentionally unmapped.
type apiSubtitle struct {
	Label  string `json:"label"`
	File   string `json:"file"`
	Source string `json:"source"`
}

// laneResult is one server's answer. A nil source with a nil error means the
// lane simply has nothing for this title.
type laneResult struct {
	server         server
	source         *apiSource
	subtitles      []apiSubtitle
	audioLanguages []string
	err            error
}

type variantStream struct {
	URL       string
	Quality   string
	Bandwidth int
	Height    int
}

func parseCoordinates(mediaID string, episode provider.Episode) (int, string, int, int, error) {
	tmdbID := episode.TMDBID
	if tmdbID <= 0 {
		var err error
		tmdbID, err = strconv.Atoi(strings.TrimSpace(mediaID))
		if err != nil {
			return 0, "", 0, 0, fmt.Errorf("invalid tmdb media ID: %w", err)
		}
		if tmdbID <= 0 {
			return 0, "", 0, 0, fmt.Errorf("invalid tmdb media ID: %q", mediaID)
		}
	}

	mediaType := provider.MediaTypeMovie
	season, epNum := 1, 1
	if episode.Season > 0 || episode.Episode > 0 {
		mediaType = provider.MediaTypeTV
		if episode.Season > 0 {
			season = episode.Season
		}
		if episode.Episode > 0 {
			epNum = episode.Episode
		}
	}
	return tmdbID, mediaType, season, epNum, nil
}

func (c *Client) fetchSources(
	ctx context.Context,
	tmdbID int,
	mediaType string,
	season, epNum int,
	updates chan<- []provider.MediaSource,
) error {
	results := make([]laneResult, len(servers))
	var wg sync.WaitGroup
	for i, srv := range servers {
		wg.Add(1)
		go func(i int, srv server) {
			defer wg.Done()
			results[i] = c.fetchLane(ctx, srv, tmdbID, mediaType, season, epNum)
		}(i, srv)
	}
	wg.Wait()

	subtitles := collectSubtitles(results)
	sources := provider.FilterDirectSources(c.buildSources(ctx, results, subtitles))
	if len(sources) == 0 {
		var lastErr error
		for _, result := range results {
			if result.err != nil {
				lastErr = result.err
			}
		}
		if lastErr != nil {
			return lastErr
		}
		return provider.ErrNoSources
	}

	select {
	case updates <- sources:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// fetchLane asks one VidGold lane for the requested title. A missing stream is
// a valid answer, not an error.
func (c *Client) fetchLane(ctx context.Context, srv server, tmdbID int, mediaType string, season, epNum int) laneResult {
	reqCtx, cancel := context.WithTimeout(ctx, timeouts.VidgoldLane)
	defer cancel()

	target := c.apiURL(mediaType, tmdbID, season, epNum, srv.key)
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, target, nil)
	if err != nil {
		return laneResult{server: srv, err: fmt.Errorf("vidgold request: %w", err)}
	}
	c.applyAPIHeaders(req, mediaType, tmdbID, season, epNum)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return laneResult{server: srv, err: fmt.Errorf("vidgold lane %s: %w", srv.key, err)}
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone {
		return laneResult{server: srv}
	}
	if resp.StatusCode != http.StatusOK {
		return laneResult{server: srv, err: &provider.HTTPError{Code: resp.StatusCode, URL: target}}
	}

	var data apiResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxAPIBytes)).Decode(&data); err != nil {
		return laneResult{server: srv, err: fmt.Errorf("vidgold lane %s decode: %w", srv.key, err)}
	}
	return laneResult{
		server:         srv,
		source:         data.Source,
		subtitles:      data.Subtitles,
		audioLanguages: spokenLanguages(data.Meta),
	}
}

// spokenLanguages canonicalizes the ISO codes the API declares for a title.
// Unknown or blank codes are dropped so no invented language reaches the
// central audio filter.
func spokenLanguages(raw json.RawMessage) []string {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var meta apiMeta
	if err := json.Unmarshal(raw, &meta); err != nil {
		return nil
	}
	codes := make([]string, 0, len(meta.SpokenLanguages))
	for _, entry := range meta.SpokenLanguages {
		codes = append(codes, entry.ISO6391)
	}
	return lang.Canonicalize(codes)
}

// apiURL builds the lane endpoint. Query parameters are sorted for stable
// request URLs.
func (c *Client) apiURL(mediaType string, tmdbID, season, epNum int, key string) string {
	params := url.Values{}
	params.Set("id", strconv.Itoa(tmdbID))
	params.Set("mode", "json")
	params.Set("sources", key)

	path := "/movie"
	if mediaType == provider.MediaTypeTV {
		path = "/tv"
		params.Set("season", strconv.Itoa(season))
		params.Set("episode", strconv.Itoa(epNum))
	}
	return c.apiBase + path + "?" + params.Encode()
}

// applyAPIHeaders supplies the browser identity the VidGold API requires. A
// request without Sec-Ch-Ua gets an all-null payload, and the stream CDNs
// reject requests without a player Origin/Referer.
func (c *Client) applyAPIHeaders(req *http.Request, mediaType string, tmdbID, season, epNum int) {
	req.Header.Set("User-Agent", config.DesktopUserAgent)
	req.Header.Set("Accept", "application/json, text/plain, */*")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	req.Header.Set("Origin", c.playerBase)
	req.Header.Set("Referer", c.playerReferer(mediaType, tmdbID, season, epNum))
	req.Header.Set("Sec-Ch-Ua", `"Chromium";v="134", "Not:A-Brand";v="24", "Google Chrome";v="134"`)
	req.Header.Set("Sec-Ch-Ua-Mobile", "?0")
	req.Header.Set("Sec-Ch-Ua-Platform", `"macOS"`)
	req.Header.Set("Sec-Fetch-Dest", "empty")
	req.Header.Set("Sec-Fetch-Mode", "cors")
	req.Header.Set("Sec-Fetch-Site", "cross-site")
}

// playerReferer mirrors the embed document URL the browser would be on when it
// calls the API.
func (c *Client) playerReferer(mediaType string, tmdbID, season, epNum int) string {
	if mediaType == provider.MediaTypeTV {
		return fmt.Sprintf("%s/embed/tv/%d/%d/%d", c.playerBase, tmdbID, season, epNum)
	}
	return fmt.Sprintf("%s/embed/movie/%d", c.playerBase, tmdbID)
}

// buildSources expands every lane answer into playable rows, keeping server
// order stable so the output does not depend on response timing. Lanes are
// processed concurrently because each HLS master needs its own probe.
func (c *Client) buildSources(ctx context.Context, results []laneResult, subtitles []provider.SubtitleOption) []provider.MediaSource {
	groups := make([][]provider.MediaSource, len(results))
	var wg sync.WaitGroup
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			groups[i] = c.laneSources(ctx, results[i], subtitles)
		}(i)
	}
	wg.Wait()

	sources := make([]provider.MediaSource, 0, len(results)*4)
	seen := make(map[string]bool)
	for _, group := range groups {
		for _, source := range group {
			if seen[source.URL] {
				continue
			}
			seen[source.URL] = true
			sources = append(sources, source)
		}
	}
	return sources
}

// sourceMeta carries the typed fields attached to every row of one lane.
type sourceMeta struct {
	backend        string
	height         int
	bitrate        int
	audioLanguages []string
	subtitles      []provider.SubtitleOption
}

// laneSources turns one server's answer into rows. Explicit MP4 qualities are
// emitted directly; HLS entries are probed for master variants so 4K tiers are
// surfaced alongside the adaptive stream.
func (c *Client) laneSources(ctx context.Context, result laneResult, subtitles []provider.SubtitleOption) []provider.MediaSource {
	if result.source == nil {
		return nil
	}

	backend := strings.TrimSpace(result.source.Label)
	if backend == "" {
		backend = result.server.label
	}
	meta := sourceMeta{
		backend:        backend,
		audioLanguages: result.audioLanguages,
		subtitles:      subtitles,
	}

	var sources []provider.MediaSource
	seen := make(map[string]bool)
	add := func(target, quality, sourceType string, meta sourceMeta) {
		if target == "" || seen[target] {
			return
		}
		seen[target] = true
		sources = append(sources, c.newMediaSource(target, quality, sourceType, meta))
	}

	if len(result.source.Qualities) > 0 {
		for _, quality := range result.source.Qualities {
			u := strings.TrimSpace(quality.URL)
			if u == "" {
				continue
			}
			sourceType := qualityType(quality, u)
			if sourceType == provider.SourceTypeHLS {
				if variants, err := c.fetchVariants(ctx, u); err != nil {
					logging.Debug("vidgold variant parsing failed; keeping HLS row", "url", logging.Redact(u), "server", result.server.label, "err", err)
				} else if len(variants) > 0 {
					bestHeight, bestBitrate := 0, 0
					for _, variant := range variants {
						bestHeight = max(bestHeight, variant.Height)
						bestBitrate = max(bestBitrate, variant.Bandwidth)
						add(variant.URL, variant.Quality, provider.SourceTypeHLS, sourceMeta{
							backend: backend, height: variant.Height, bitrate: variant.Bandwidth,
							audioLanguages: result.audioLanguages, subtitles: subtitles,
						})
					}
					meta.height, meta.bitrate = bestHeight, bestBitrate
					add(u, "Auto", provider.SourceTypeHLS, meta)
					continue
				}
			}
			label := qualityLabel(quality, result.source.Height)
			meta.height, meta.bitrate = heightOfLabel(label), 0
			add(u, label, sourceType, meta)
		}
		return sources
	}

	masterURL := strings.TrimSpace(result.source.URL)
	if masterURL == "" {
		return nil
	}

	variants, err := c.fetchVariants(ctx, masterURL)
	if err != nil {
		logging.Debug("vidgold variant parsing failed; falling back to master", "url", logging.Redact(masterURL), "server", result.server.label, "err", err)
	}
	bestHeight, bestBitrate := 0, 0
	for _, variant := range variants {
		bestHeight = max(bestHeight, variant.Height)
		bestBitrate = max(bestBitrate, variant.Bandwidth)
		add(variant.URL, variant.Quality, provider.SourceTypeHLS, sourceMeta{
			backend: backend, height: variant.Height, bitrate: variant.Bandwidth,
			audioLanguages: result.audioLanguages, subtitles: subtitles,
		})
	}
	// Always offer the adaptive master alongside individual tiers.
	meta.height, meta.bitrate = bestHeight, bestBitrate
	add(masterURL, "Auto", provider.SourceTypeHLS, meta)
	return sources
}

// qualityLabel renders an API quality row. "Auto" rows use the lane's declared
// height when it is plausible so a 1080p auto stream is not shown as an unknown
// tier; flag-like values below minDeclaredHeight stay "Auto".
func qualityLabel(quality apiQuality, height int) string {
	label := strings.TrimSpace(quality.Quality)
	if label == "" || strings.EqualFold(label, "auto") {
		if height >= minDeclaredHeight {
			return resolutionLabel(height)
		}
		if label == "" {
			return "Auto"
		}
	}
	return label
}

// qualityType maps the API's container field to Kari's stream vocabulary,
// inferring from the URL when the field is absent or unknown.
func qualityType(quality apiQuality, rawURL string) string {
	switch strings.ToLower(strings.TrimSpace(quality.Type)) {
	case provider.SourceTypeHLS, provider.SourceTypeM3U8:
		return provider.SourceTypeHLS
	case provider.SourceTypeMP4:
		return provider.SourceTypeMP4
	}
	if strings.Contains(strings.ToLower(rawURL), ".m3u8") {
		return provider.SourceTypeHLS
	}
	return provider.SourceTypeMP4
}

func (c *Client) newMediaSource(target, quality, sourceType string, meta sourceMeta) provider.MediaSource {
	return provider.MediaSource{
		URL:       target,
		Quality:   quality,
		Height:    meta.height,
		Bitrate:   meta.bitrate,
		Backend:   meta.backend,
		Type:      sourceType,
		Referer:   c.playerBase + "/",
		UserAgent: config.DesktopUserAgent,
		Audio: provider.AudioInfo{
			Languages: meta.audioLanguages,
		},
		Subtitles: meta.subtitles,
	}
}

// heightOfLabel resolves an explicit quality label to a pixel height. Adaptive
// labels stay unknown: the manifest picks their tier at playback time.
func heightOfLabel(label string) int {
	if kit.IsAdaptiveQuality(label) {
		return 0
	}
	return kit.QualityHeight(label)
}

// resolutionLabel buckets a pixel height into the shared canonical label.
// Heights such as the API's 800/804 auto lanes fall into their nearest tier.
func resolutionLabel(height int) string {
	return kit.QualityLabelFromHeight(height)
}

// fetchVariants reads an HLS master and returns its individual quality tiers,
// highest first. Non-master playlists simply yield no variants.
func (c *Client) fetchVariants(ctx context.Context, masterURL string) ([]variantStream, error) {
	reqCtx, cancel := context.WithTimeout(ctx, timeouts.VidgoldMaster)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, masterURL, nil)
	if err != nil {
		return nil, fmt.Errorf("vidgold master request: %w", err)
	}
	req.Header.Set("User-Agent", config.DesktopUserAgent)
	req.Header.Set("Referer", c.playerBase+"/")
	req.Header.Set("Origin", c.playerBase)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch master: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, &provider.HTTPError{Code: resp.StatusCode, URL: masterURL}
	}

	baseURL, err := url.Parse(masterURL)
	if err != nil {
		return nil, fmt.Errorf("parse master url: %w", err)
	}

	var (
		scanner   = bufio.NewScanner(io.LimitReader(resp.Body, maxPlaylistBytes))
		variants  []variantStream
		curHeight int
		curBW     int
		hasStream bool
	)

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		if strings.HasPrefix(line, "#EXT-X-STREAM-INF:") {
			hasStream = true
			curHeight = 0
			curBW = 0

			if m := reResolution.FindStringSubmatch(line); len(m) == 3 {
				if h, err := strconv.Atoi(m[2]); err == nil {
					curHeight = h
				}
			}
			if m := reBandwidth.FindStringSubmatch(line); len(m) == 2 {
				if bw, err := strconv.Atoi(m[1]); err == nil {
					curBW = bw
				}
			}
			continue
		}

		if hasStream && !strings.HasPrefix(line, "#") {
			hasStream = false
			refURL, err := url.Parse(line)
			if err != nil {
				continue
			}
			fullURL := baseURL.ResolveReference(refURL).String()
			label := resolutionLabel(curHeight)
			if curHeight == 0 {
				label = "1080p"
			}

			variants = append(variants, variantStream{
				URL:       fullURL,
				Quality:   label,
				Bandwidth: curBW,
				Height:    curHeight,
			})
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan playlist: %w", err)
	}

	sort.Slice(variants, func(i, j int) bool {
		if variants[i].Height != variants[j].Height {
			return variants[i].Height > variants[j].Height
		}
		return variants[i].Bandwidth > variants[j].Bandwidth
	})
	return variants, nil
}

// collectSubtitles merges the subtitle lists returned by every lane. Lanes
// that have no stream still publish the title's subtitle set. The upstream
// list carries nearly a hundred entries for the same handful of languages, so
// tracks are keyed by canonical language plus source; lanes that omit a source
// id fall back to the URL, preserving distinct tracks without one.
func collectSubtitles(results []laneResult) []provider.SubtitleOption {
	var out []provider.SubtitleOption
	seen := make(map[string]bool)

	for _, result := range results {
		for _, item := range result.subtitles {
			u := strings.TrimSpace(item.File)
			if u == "" {
				continue
			}

			clean := strings.TrimSpace(reSubtitleLabelTrim.ReplaceAllString(item.Label, ""))
			norm := lang.FromLabel(clean)
			if norm == "" {
				norm = lang.FromLabel(item.Label)
			}
			if norm == "" {
				norm = "und"
			}
			source := strings.ToLower(strings.TrimSpace(item.Source))
			if source == "" {
				source = u
			}
			key := norm + "\x00" + source
			if seen[key] {
				continue
			}
			seen[key] = true

			out = append(out, provider.SubtitleOption{
				URL:      u,
				Language: norm,
			})
		}
	}
	return out
}
