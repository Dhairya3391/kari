package cinecat

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"kari/internal/config"
	"kari/internal/lang"
	"kari/internal/logging"
	"kari/internal/provider"
	"kari/internal/provider/kit"
)

var (
	reResolution        = regexp.MustCompile(`(?i)\bRESOLUTION=(\d+)x(\d+)\b`)
	reBandwidth         = regexp.MustCompile(`(?i)\bBANDWIDTH=(\d+)\b`)
	reSubtitleLabelTrim = regexp.MustCompile(`(?i)\s*(?:hi\d*|\d+)$`)
	reLaneName          = regexp.MustCompile(`^[A-Za-z0-9_-]{1,40}$`)
)

// aphroditeBackend labels Aphrodite rows in the source list.
const aphroditeBackend = "Aphrodite"

// errCapRequired marks the upstream cap.js proof-of-work gate on the ZCDN and
// sources lanes. Those lanes are optional: the ungated Aphrodite edge still
// provides playable sources, so this error only demotes the gated lanes. It
// wraps ErrNoSources so a fully gated upstream stays a benign "empty catalog"
// answer and never trips the provider circuit breaker.
var errCapRequired = fmt.Errorf("cinecat: upstream cap pass required: %w", provider.ErrNoSources)

type zcdnResponse struct {
	Ready  bool   `json:"ready"`
	URL    string `json:"url"`
	Status string `json:"status,omitempty"`
	RB     string `json:"rb,omitempty"`
}

// aphroditeResponse is Cinecat's Aphrodite content lookup result. URL is a
// short-lived HLS master on the totallyacdn.cc edge; Type is "hls" for every
// playable entry.
type aphroditeResponse struct {
	Found bool   `json:"found"`
	URL   string `json:"url"`
	Type  string `json:"type"`
}

type variantStream struct {
	URL       string
	Quality   string
	Bandwidth int
	Height    int
}

// sourceCandidate is one playable playlist discovered upstream.
// Backend identifies the lane that produced it; empty means the ZCDN edge.
// Label is the lane's own quality declaration, used when the playlist turns
// out to be a single rendition rather than a master ladder.
type sourceCandidate struct {
	URL     string
	Backend string
	Label   string
}

// laneStream is one HLS playlist a lane advertised. A lane may return a
// master ladder or a single-rendition playlist; Label disambiguates the latter.
type laneStream struct {
	URL   string
	Label string
}

type sourcesListResponse struct {
	Sources []struct {
		Name string `json:"name"`
	} `json:"sources"`
}

type laneStreamsResponse struct {
	Streams []laneStreamResponse `json:"streams"`
}

type laneStreamResponse struct {
	Kind  string `json:"kind"`
	URL   string `json:"url"`
	Label string `json:"label"`
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

func (c *Client) newRequest(ctx context.Context, target string) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, fmt.Errorf("cinecat request: %w", err)
	}
	req.Header.Set("User-Agent", config.DesktopUserAgent)
	req.Header.Set("Referer", c.siteBase+"/")
	req.Header.Set("Origin", c.siteBase)
	return req, nil
}

func (c *Client) fetchSources(
	ctx context.Context,
	tmdbID int,
	mediaType string,
	season, epNum int,
	updates chan<- []provider.MediaSource,
) error {
	var (
		wg             sync.WaitGroup
		masterURL      string
		zcdnErr        error
		aphroditeURL   string
		aphroditeErr   error
		laneCandidates []sourceCandidate
		lanesErr       error
		subtitles      []provider.SubtitleOption
	)

	wg.Add(4)
	go func() {
		defer wg.Done()
		masterURL, zcdnErr = c.fetchZCDN(ctx, tmdbID, mediaType, season, epNum)
	}()

	go func() {
		defer wg.Done()
		aphroditeURL, aphroditeErr = c.fetchAphrodite(ctx, tmdbID, mediaType, season, epNum)
		if aphroditeErr != nil && !errors.Is(aphroditeErr, provider.ErrNoSources) {
			logging.Debug("cinecat aphrodite failed", "tmdb", tmdbID, "err", aphroditeErr)
		}
	}()

	go func() {
		defer wg.Done()
		laneCandidates, lanesErr = c.fetchSourceLanes(ctx, tmdbID, mediaType, season, epNum)
		if lanesErr != nil && !errors.Is(lanesErr, provider.ErrNoSources) {
			logging.Debug("cinecat source lanes failed", "tmdb", tmdbID, "err", lanesErr)
		}
	}()

	go func() {
		defer wg.Done()
		var err error
		subtitles, err = c.fetchSubtitles(ctx, tmdbID, mediaType, season, epNum)
		if err != nil {
			logging.Debug("cinecat subtitles failed", "tmdb", tmdbID, "err", err)
		}
	}()

	wg.Wait()

	candidates := make([]sourceCandidate, 0, len(laneCandidates)+2)
	if strings.TrimSpace(masterURL) != "" {
		candidates = append(candidates, sourceCandidate{URL: masterURL})
	}
	if strings.TrimSpace(aphroditeURL) != "" {
		candidates = append(candidates, sourceCandidate{URL: aphroditeURL, Backend: aphroditeBackend})
	}
	candidates = append(candidates, laneCandidates...)
	candidates = dedupeCandidates(candidates)

	if len(candidates) == 0 {
		if zcdnErr != nil {
			return zcdnErr
		}
		if aphroditeErr != nil {
			return aphroditeErr
		}
		if lanesErr != nil {
			return lanesErr
		}
		return provider.ErrNoSources
	}

	sources := c.buildSources(ctx, candidates, subtitles)
	if len(sources) == 0 {
		return provider.ErrNoSources
	}

	select {
	case updates <- sources:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// dedupeCandidates drops repeated playlists while preserving upstream order.
func dedupeCandidates(in []sourceCandidate) []sourceCandidate {
	out := in[:0]
	seen := make(map[string]bool, len(in))
	for _, cand := range in {
		u := strings.TrimSpace(cand.URL)
		if u == "" || seen[u] {
			continue
		}
		seen[u] = true
		out = append(out, sourceCandidate{URL: u, Backend: cand.Backend, Label: cand.Label})
	}
	return out
}

// buildSources expands every candidate playlist into quality tiers in parallel,
// keeping candidate order stable so output does not depend on response timing.
func (c *Client) buildSources(ctx context.Context, candidates []sourceCandidate, subtitles []provider.SubtitleOption) []provider.MediaSource {
	groups := make([][]provider.MediaSource, len(candidates))
	var wg sync.WaitGroup
	for i, cand := range candidates {
		wg.Add(1)
		go func(i int, cand sourceCandidate) {
			defer wg.Done()
			variants, err := c.fetchVariants(ctx, cand.URL)
			if err != nil {
				logging.Debug("cinecat variant parsing failed; falling back to master", "url", cand.URL, "backend", cand.Backend, "err", err)
			}
			groups[i] = c.candidateSources(cand, variants, subtitles)
		}(i, cand)
	}
	wg.Wait()

	sources := make([]provider.MediaSource, 0, len(candidates)*4)
	for _, group := range groups {
		sources = append(sources, group...)
	}
	return sources
}

func (c *Client) candidateSources(cand sourceCandidate, variants []variantStream, subtitles []provider.SubtitleOption) []provider.MediaSource {
	if len(variants) == 0 {
		if quality := laneQuality(cand.Label); quality != "" {
			return []provider.MediaSource{c.newMediaSource(cand.URL, quality, cand.Backend, subtitles)}
		}
	}

	autoLabel := "Auto (Adaptive)"
	if cand.Backend != "" {
		autoLabel = fmt.Sprintf("Auto (%s)", cand.Backend)
	}
	sources := make([]provider.MediaSource, 0, len(variants)+1)
	for _, v := range variants {
		sources = append(sources, c.newMediaSource(v.URL, v.Quality, cand.Backend, subtitles))
	}
	// Always offer the adaptive master alongside individual tiers.
	sources = append(sources, c.newMediaSource(cand.URL, autoLabel, cand.Backend, subtitles))
	return sources
}

// laneQuality canonicalizes a lane's declared stream quality through the same
// height-to-label mapping the ZCDN rows use, so a 4K lane and a 4K edge tier
// cannot render as two different spellings. Unknown or absent declarations
// report an empty label so callers keep the lane name.
func laneQuality(raw string) string {
	if strings.TrimSpace(raw) == "" {
		return ""
	}
	height := kit.QualityHeight(raw)
	if height <= 0 {
		return ""
	}
	return resolutionLabel(height)
}

func (c *Client) newMediaSource(target, quality, backend string, subtitles []provider.SubtitleOption) provider.MediaSource {
	return provider.MediaSource{
		URL:            target,
		Quality:        quality,
		Backend:        backend,
		Type:           provider.SourceTypeHLS,
		Referer:        c.siteBase + "/",
		UserAgent:      config.DesktopUserAgent,
		SuppressOrigin: true,
		Subtitles:      subtitles,
	}
}

// fetchSourceLanes fans out across every lane the sources API advertises and
// collects their HLS playlists. One broken lane never fails the resolution.
func (c *Client) fetchSourceLanes(ctx context.Context, tmdbID int, mediaType string, season, epNum int) ([]sourceCandidate, error) {
	names, err := c.fetchLaneNames(ctx)
	if err != nil {
		return nil, err
	}
	if len(names) == 0 {
		return nil, provider.ErrNoSources
	}

	results := make([][]laneStream, len(names))
	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		lastErr error
	)
	for i, name := range names {
		wg.Add(1)
		go func(i int, name string) {
			defer wg.Done()
			streams, err := c.fetchLaneStreams(ctx, name, tmdbID, mediaType, season, epNum)
			if err != nil {
				if !errors.Is(err, provider.ErrNoSources) {
					mu.Lock()
					lastErr = err
					mu.Unlock()
				}
				return
			}
			results[i] = streams
		}(i, name)
	}
	wg.Wait()

	var candidates []sourceCandidate
	for i, name := range names {
		for _, stream := range results[i] {
			candidates = append(candidates, sourceCandidate{
				URL:     stream.URL,
				Backend: laneLabel(name),
				Label:   stream.Label,
			})
		}
	}
	if len(candidates) == 0 {
		if lastErr != nil {
			return nil, lastErr
		}
		return nil, provider.ErrNoSources
	}
	return candidates, nil
}

// laneLabel turns an upstream lane identifier into a display-friendly backend name.
func laneLabel(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}
	runes := []rune(name)
	runes[0] = unicode.ToUpper(runes[0])
	return string(runes)
}

// fetchLaneNames asks the sources API which lanes are currently available.
func (c *Client) fetchLaneNames(ctx context.Context) ([]string, error) {
	if strings.TrimSpace(c.sourcesBase) == "" {
		return nil, provider.ErrNoSources
	}

	reqCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()

	target := c.sourcesBase + "/sources"
	req, err := c.newRequest(reqCtx, target)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("cinecat sources: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized {
		return nil, errCapRequired
	}
	if resp.StatusCode != http.StatusOK {
		return nil, &provider.HTTPError{Code: resp.StatusCode, URL: target}
	}

	var data sourcesListResponse
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, fmt.Errorf("cinecat sources decode: %w", err)
	}

	names := make([]string, 0, len(data.Sources))
	for _, src := range data.Sources {
		name := strings.TrimSpace(src.Name)
		if reLaneName.MatchString(name) {
			names = append(names, name)
		}
	}
	return names, nil
}

// fetchLaneStreams resolves one lane's HLS playlists for the requested title.
func (c *Client) fetchLaneStreams(ctx context.Context, lane string, tmdbID int, mediaType string, season, epNum int) ([]laneStream, error) {
	reqCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()

	params := url.Values{}
	params.Set("tmdb", strconv.Itoa(tmdbID))
	params.Set("type", mediaType)
	if mediaType == provider.MediaTypeTV {
		params.Set("season", strconv.Itoa(season))
		params.Set("episode", strconv.Itoa(epNum))
	}

	base, err := url.Parse(c.sourcesBase + "/")
	if err != nil {
		return nil, fmt.Errorf("parse sources base: %w", err)
	}
	target := fmt.Sprintf("%s/sources/%s?%s", c.sourcesBase, url.PathEscape(lane), params.Encode())

	req, err := c.newRequest(reqCtx, target)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("cinecat lane %s: %w", lane, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil, provider.ErrNoSources
	}
	if resp.StatusCode != http.StatusOK {
		return nil, &provider.HTTPError{Code: resp.StatusCode, URL: target}
	}

	var data laneStreamsResponse
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, fmt.Errorf("cinecat lane %s decode: %w", lane, err)
	}

	var streams []laneStream
	for _, stream := range data.Streams {
		if stream.Kind != "" && stream.Kind != "hls" {
			continue
		}
		raw := strings.TrimSpace(stream.URL)
		if raw == "" {
			continue
		}
		ref, err := url.Parse(raw)
		if err != nil {
			continue
		}
		streams = append(streams, laneStream{
			URL:   base.ResolveReference(ref).String(),
			Label: strings.TrimSpace(stream.Label),
		})
	}
	return streams, nil
}

// fetchAphrodite asks Cinecat's Aphrodite content API for the title's HLS
// master. It is independent of ZCDN: a ZCDN miss or a missing Aphrodite entry
// never prevents the other from serving playback.
func (c *Client) fetchAphrodite(ctx context.Context, tmdbID int, mediaType string, season, epNum int) (string, error) {
	if strings.TrimSpace(c.aphroditeBase) == "" {
		return "", provider.ErrNoSources
	}

	reqCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	var target string
	if mediaType == provider.MediaTypeTV {
		target = fmt.Sprintf("%s/content/tv/%d/%d/%d", c.aphroditeBase, tmdbID, season, epNum)
	} else {
		target = fmt.Sprintf("%s/content/movie/%d", c.aphroditeBase, tmdbID)
	}

	req, err := c.newRequest(reqCtx, target)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("cinecat aphrodite: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return "", provider.ErrNoSources
	}
	if resp.StatusCode != http.StatusOK {
		return "", &provider.HTTPError{Code: resp.StatusCode, URL: target}
	}

	var data aphroditeResponse
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return "", fmt.Errorf("cinecat aphrodite decode: %w", err)
	}

	playlist := strings.TrimSpace(data.URL)
	if !data.Found || playlist == "" || data.Type != "hls" {
		return "", provider.ErrNoSources
	}
	return playlist, nil
}

func (c *Client) fetchZCDN(ctx context.Context, tmdbID int, mediaType string, season, epNum int) (string, error) {
	reqCtx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()

	params := url.Values{}
	params.Set("tmdb", strconv.Itoa(tmdbID))
	params.Set("type", mediaType)
	if mediaType == provider.MediaTypeTV {
		params.Set("season", strconv.Itoa(season))
		params.Set("episode", strconv.Itoa(epNum))
	}

	target := fmt.Sprintf("%s/cdn?%s", c.cdnBase, params.Encode())
	req, err := c.newRequest(reqCtx, target)
	if err != nil {
		return "", err
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("cinecat cdn: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusAccepted {
		// 202 Accepted signifies stream is currently being transcoded/built upstream.
		return "", provider.ErrNoSources
	}
	if resp.StatusCode == http.StatusUnauthorized {
		return "", errCapRequired
	}
	if resp.StatusCode != http.StatusOK {
		return "", &provider.HTTPError{Code: resp.StatusCode, URL: target}
	}

	var data zcdnResponse
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return "", fmt.Errorf("cinecat decode: %w", err)
	}

	if !data.Ready || strings.TrimSpace(data.URL) == "" {
		return "", provider.ErrNoSources
	}
	masterURL := strings.TrimSpace(data.URL)
	if rb := strings.TrimSpace(data.RB); rb != "" {
		sep := "?"
		if strings.Contains(masterURL, "?") {
			sep = "&"
		}
		masterURL += sep + "_rb=" + url.QueryEscape(rb)
	}
	return masterURL, nil
}

// resolutionLabel buckets a pixel height into the shared canonical label.
func resolutionLabel(height int) string {
	return kit.QualityLabelFromHeight(height)
}

func (c *Client) fetchVariants(ctx context.Context, masterURL string) ([]variantStream, error) {
	reqCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()

	req, err := c.newRequest(reqCtx, masterURL)
	if err != nil {
		return nil, err
	}

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
		scanner   = bufio.NewScanner(resp.Body)
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

	// Sort variants highest resolution and bandwidth first.
	sort.Slice(variants, func(i, j int) bool {
		if variants[i].Height != variants[j].Height {
			return variants[i].Height > variants[j].Height
		}
		return variants[i].Bandwidth > variants[j].Bandwidth
	})

	return variants, nil
}

func (c *Client) fetchSubtitles(
	ctx context.Context,
	tmdbID int,
	mediaType string,
	season, epNum int,
) ([]provider.SubtitleOption, error) {
	reqCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()

	var target string
	if mediaType == provider.MediaTypeTV {
		target = fmt.Sprintf("%s/v1/tv/%d/%d/%d", c.subsBase, tmdbID, season, epNum)
	} else {
		target = fmt.Sprintf("%s/v1/movie/%d", c.subsBase, tmdbID)
	}

	req, err := c.newRequest(reqCtx, target)
	if err != nil {
		return nil, err
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, &provider.HTTPError{Code: resp.StatusCode, URL: target}
	}

	var items []struct {
		Label string `json:"label"`
		File  string `json:"file"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&items); err != nil {
		return nil, fmt.Errorf("granite decode: %w", err)
	}

	var out []provider.SubtitleOption
	seen := make(map[string]bool)

	for _, item := range items {
		u := strings.TrimSpace(item.File)
		if u == "" {
			continue
		}
		clean := strings.TrimSpace(reSubtitleLabelTrim.ReplaceAllString(item.Label, ""))
		norm := lang.Normalize(clean)
		if norm == "" {
			norm = lang.Normalize(item.Label)
		}
		if norm == "" {
			norm = "und"
		}

		key := u + ":" + norm
		if seen[key] {
			continue
		}
		seen[key] = true

		out = append(out, provider.SubtitleOption{
			URL:      u,
			Language: norm,
		})
	}

	return out, nil
}
