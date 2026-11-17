package mapple

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
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
)

// Mapple rotates the subtitles server-action hash on deploys, so the live
// value is scraped from the watch page HTML. The constant is only a fallback
// for pages that do not expose a parseable ID. actionIDScanRadius bounds how
// far from an action marker an ID may appear in that HTML.
const (
	nextActionSubtitles = "40b40d9f0a6d65ec7ecc1fe5d977b0df335f81263c"
	actionIDScanRadius  = 200
)

var (
	reResolution  = regexp.MustCompile(`(?i)\bRESOLUTION=(\d+)x(\d+)\b`)
	reBandwidth   = regexp.MustCompile(`(?i)\bBANDWIDTH=(\d+)\b`)
	reHexActionID = regexp.MustCompile(`[0-9a-f]{40,42}`)
)

type hlsVariant struct {
	URL       string
	Quality   string
	Bandwidth int
	Height    int
}

type sessionResponse struct {
	Token string `json:"token"`
}

type mediaSessionResponse struct {
	Success     bool      `json:"success"`
	Token       string    `json:"token,omitempty"`
	RequiresPow bool      `json:"requiresPow,omitempty"`
	Pow         powParams `json:"pow,omitempty"`
}

type powParams struct {
	ChallengeID string `json:"challengeId"`
	Challenge   string `json:"challenge"`
	Difficulty  int    `json:"difficulty"`
}

type encryptResponse struct {
	URL string `json:"url"`
}

type streamResponse struct {
	Success bool `json:"success"`
	Data    struct {
		StreamURL string `json:"stream_url"`
		Source    string `json:"source"`
	} `json:"data"`
}

type subtitleTrack struct {
	URL   string `json:"url"`
	Lang  string `json:"lang"`
	Label string `json:"label"`
}

// subtitleFetchParams groups the per-title inputs of a subtitles server-action
// call, including the action ID scraped from the watch page.
type subtitleFetchParams struct {
	watchPath string
	tmdbID    int
	mediaType string
	season    int
	epNum     int
	actionID  string
}

// actionIDSource bundles the inputs needed to resolve the live subtitles
// server-action ID from a watch page and the scripts it references.
type actionIDSource struct {
	ctx           context.Context
	sessionClient *http.Client
	siteBase      string
	watchURL      string
	html          string
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

// solvePow finds a numeric nonce such that SHA256(challenge + nonce) has at
func solvePow(ctx context.Context, challenge string, difficulty int) string {
	if difficulty <= 0 || difficulty > 256 {
		return ""
	}

	fullBytes := difficulty / 8
	remBits := difficulty % 8
	mask := byte(0)
	if remBits > 0 {
		mask = byte(0xff << (8 - remBits))
	}

	var nonce int64
	buf := make([]byte, 0, len(challenge)+24)

	for {
		if nonce&0xfff == 0 && ctx.Err() != nil {
			return ""
		}

		buf = buf[:0]
		buf = append(buf, challenge...)
		buf = strconv.AppendInt(buf, nonce, 10)

		sum := sha256.Sum256(buf)
		match := true
		for i := range fullBytes {
			if sum[i] != 0 {
				match = false
				break
			}
		}
		if match && remBits > 0 {
			if (sum[fullBytes] & mask) != 0 {
				match = false
			}
		}
		if match {
			return strconv.FormatInt(nonce, 10)
		}
		nonce++
	}
}

// fetchWatchPage loads the watch page once to establish session cookies and to
// capture the HTML that advertises the current Next.js server-action IDs.
func fetchWatchPage(ctx context.Context, sessionClient *http.Client, watchURL string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, watchURL, nil)
	if err != nil {
		return "", fmt.Errorf("mapple watch page request: %w", err)
	}
	req.Header.Set("User-Agent", config.DesktopUserAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml")

	resp, err := sessionClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("mapple watch page visit: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("mapple watch page status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return "", fmt.Errorf("mapple read watch page: %w", err)
	}
	return string(body), nil
}

// setBrowserFetchHeaders adds the same-origin fetch headers a browser sends to
// Mapple's protected API endpoints. The site rejects those calls with a
// "Site session required" error when Origin and Sec-Fetch-* are missing. HLS
// media fetches must not use them: the CDN serves playlists directly and the
// player derives Origin from Referer on its own.
func setBrowserFetchHeaders(req *http.Request, siteBase string) {
	req.Header.Set("Origin", siteBase)
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	req.Header.Set("Sec-Fetch-Mode", "cors")
	req.Header.Set("Sec-Fetch-Dest", "empty")
}

// extractNextActionID returns the subtitles server-action ID advertised by the
// watch page HTML. Next.js ships the ID beside the action name in its client
// reference data, so extraction looks for a hex ID near a known marker and
// falls back to the last known constant when the page exposes none.
func extractNextActionID(html string) string {
	for _, marker := range []string{"fetchSubtitlesAction", "Next-Action", "next_action", "actionId"} {
		if id := actionIDNear(html, marker); id != "" {
			return id
		}
	}
	return nextActionSubtitles
}

// actionIDNear returns the hex ID closest to marker in html, or empty when the
// marker is absent or no ID-like token sits near it.
func actionIDNear(html, marker string) string {
	idx := strings.Index(strings.ToLower(html), strings.ToLower(marker))
	if idx < 0 {
		return ""
	}
	start := idx - actionIDScanRadius
	if start < 0 {
		start = 0
	}
	end := idx + len(marker) + actionIDScanRadius
	if end > len(html) {
		end = len(html)
	}
	window := html[start:end]

	matches := reHexActionID.FindAllStringIndex(window, -1)
	if len(matches) == 0 {
		return ""
	}
	markerPos := idx - start
	best, bestDist := 0, len(html)
	for i, m := range matches {
		dist := m[0] - markerPos
		if dist < 0 {
			dist = -dist
		}
		if dist < bestDist {
			best, bestDist = i, dist
		}
	}
	m := matches[best]
	return window[m[0]:m[1]]
}

// resolveSubtitleActionID returns the live subtitles server-action ID. Next.js
// ships the ID in a linked script chunk, so those chunks are check first; the
// watch page markers and the known constant remain the last-resort fallbacks.
func resolveSubtitleActionID(src actionIDSource) string {
	if id := scanChunksForActionID(src); id != "" {
		return id
	}
	return extractNextActionID(src.html)
}

// scanChunksForActionID fetches the same-origin scripts referenced by the watch
// page and returns the first subtitles action ID they advertise. Fetching is
// bounded by chunk count, body size, and worker count so one playback cannot
// turn into an unbounded crawl.
func scanChunksForActionID(src actionIDSource) string {
	const (
		maxChunkURLs = 48
		chunkWorkers = 6
	)
	chunkURLs := scriptURLs(src.html, src.watchURL)
	if len(chunkURLs) == 0 {
		return ""
	}
	if len(chunkURLs) > maxChunkURLs {
		chunkURLs = chunkURLs[:maxChunkURLs]
	}

	ctx, cancel := context.WithCancel(src.ctx)
	defer cancel()

	jobs := make(chan string)
	found := make(chan string, len(chunkURLs))
	var wg sync.WaitGroup
	for i := 0; i < chunkWorkers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for chunkURL := range jobs {
				if ctx.Err() != nil {
					return
				}
				if id := fetchChunkActionID(ctx, src, chunkURL); id != "" {
					select {
					case found <- id:
					default:
					}
				}
			}
		}()
	}

feed:
	for _, chunkURL := range chunkURLs {
		select {
		case jobs <- chunkURL:
		case <-ctx.Done():
			break feed
		}
	}
	close(jobs)
	wg.Wait()
	close(found)

	for id := range found {
		if id != "" {
			return id
		}
	}
	return ""
}

// scriptURLs returns deduplicated same-origin JavaScript URLs referenced by
// the watch page, including chunk paths embedded in Next.js flight data.
func scriptURLs(html, pageURL string) []string {
	const maxScriptURLs = 64
	base, err := url.Parse(pageURL)
	if err != nil {
		return nil
	}
	normalized := strings.ReplaceAll(html, `\/`, `/`)
	reChunk := regexp.MustCompile(`/_next/static/chunks/[A-Za-z0-9_.+\-]+\.js(?:\?[^"'\\<>\s]*)?`)
	reSrc := regexp.MustCompile(`(?i)<script\b[^>]*\bsrc\s*=\s*(?:"([^"]+)"|'([^']+)')`)

	seen := make(map[string]struct{})
	var out []string
	add := func(raw string) {
		if len(out) >= maxScriptURLs {
			return
		}
		raw = strings.ReplaceAll(strings.TrimSpace(raw), `\"`, `"`)
		if raw == "" {
			return
		}
		ref, err := url.Parse(raw)
		if err != nil {
			return
		}
		resolved := base.ResolveReference(ref)
		if resolved.Scheme != base.Scheme || resolved.Host != base.Host {
			return
		}
		if !strings.HasSuffix(resolved.Path, ".js") {
			return
		}
		if _, ok := seen[resolved.String()]; ok {
			return
		}
		seen[resolved.String()] = struct{}{}
		out = append(out, resolved.String())
	}

	for _, match := range reChunk.FindAllString(normalized, -1) {
		add(match)
	}
	for _, match := range reSrc.FindAllStringSubmatch(normalized, -1) {
		if match[1] != "" {
			add(match[1])
		} else {
			add(match[2])
		}
	}
	return out
}

// fetchChunkActionID downloads one script chunk and returns the subtitles
// action ID advertised next to its createServerReference call, if any.
func fetchChunkActionID(ctx context.Context, src actionIDSource, chunkURL string) string {
	const chunkReadLimit = 1 << 20
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, chunkURL, nil)
	if err != nil {
		return ""
	}
	req.Header.Set("User-Agent", config.DesktopUserAgent)
	req.Header.Set("Referer", src.watchURL)
	setBrowserFetchHeaders(req, src.siteBase)

	resp, err := src.sessionClient.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return ""
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, chunkReadLimit))
	if err != nil {
		return ""
	}
	return actionIDNear(string(body), "fetchSubtitlesAction")
}

func (c *Client) fetchSources(
	ctx context.Context,
	tmdbID int,
	mediaType string,
	season, epNum int,
	updates chan<- []provider.MediaSource,
) error {
	var (
		watchPath string
		tvSlug    string
	)

	if mediaType == provider.MediaTypeTV {
		tvSlug = fmt.Sprintf("%d-%d", season, epNum)
		watchPath = fmt.Sprintf("watch/tv/%d-%s", tmdbID, tvSlug)
	} else {
		watchPath = fmt.Sprintf("watch/movie/%d", tmdbID)
	}

	jar, err := cookiejar.New(nil)
	if err != nil {
		return fmt.Errorf("mapple jar: %w", err)
	}
	sessionClient := *c.httpClient
	sessionClient.Jar = jar

	watchURL := fmt.Sprintf("%s/%s", c.siteBase, watchPath)
	watchHTML, err := fetchWatchPage(ctx, &sessionClient, watchURL)
	if err != nil {
		return err
	}
	actionID := resolveSubtitleActionID(actionIDSource{
		ctx:           ctx,
		sessionClient: &sessionClient,
		siteBase:      c.siteBase,
		watchURL:      watchURL,
		html:          watchHTML,
	})

	var (
		wg        sync.WaitGroup
		subtitles []provider.SubtitleOption
	)

	wg.Add(1)
	go func() {
		defer wg.Done()
		var err error
		subtitles, err = c.fetchSubtitles(ctx, &sessionClient, subtitleFetchParams{
			watchPath: watchPath,
			tmdbID:    tmdbID,
			mediaType: mediaType,
			season:    season,
			epNum:     epNum,
			actionID:  actionID,
		})
		if err != nil {
			logging.Debug("mapple subtitles failed", "tmdb", tmdbID, "err", err)
		}
	}()

	sources, err := c.resolveStreamURL(ctx, &sessionClient, watchURL, tmdbID, mediaType, tvSlug)
	wg.Wait()

	if err != nil {
		return err
	}
	if len(sources) == 0 {
		return provider.ErrNoSources
	}

	// Attach subtitles to resolved sources
	for i := range sources {
		sources[i].Subtitles = subtitles
	}

	if updates != nil {
		updates <- sources
	}
	return nil
}

func (c *Client) resolveStreamURL(
	ctx context.Context,
	sessionClient *http.Client,
	watchURL string,
	tmdbID int,
	mediaType string,
	tvSlug string,
) ([]provider.MediaSource, error) {

	sessReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.siteBase+"/api/ui/session", nil)
	if err != nil {
		return nil, fmt.Errorf("mapple session request: %w", err)
	}
	sessReq.Header.Set("User-Agent", config.DesktopUserAgent)
	sessReq.Header.Set("Referer", watchURL)
	sessReq.Header.Set("Accept", "application/json")
	setBrowserFetchHeaders(sessReq, c.siteBase)

	sessResp, err := sessionClient.Do(sessReq)
	if err != nil {
		return nil, fmt.Errorf("mapple session call: %w", err)
	}
	defer sessResp.Body.Close()

	if sessResp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(sessResp.Body, 512))
		return nil, fmt.Errorf("mapple session status %d: %s", sessResp.StatusCode, strings.TrimSpace(string(body)))
	}

	var session sessionResponse
	if err := json.NewDecoder(sessResp.Body).Decode(&session); err != nil || session.Token == "" {
		return nil, fmt.Errorf("mapple invalid session token: %w", err)
	}

	if err := c.syncSession(ctx, sessionClient, watchURL); err != nil {
		logging.Debug("mapple session sync failed; continuing", "err", err)
	}

	// Step 3: Request media session token (with PoW if required)
	mediaReqPayload, _ := json.Marshal(map[string]any{
		"mediaId":      tmdbID,
		"mediaType":    mediaType,
		"tv_slug":      tvSlug,
		"requestToken": session.Token,
	})

	mediaReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.siteBase+"/api/media-session", bytes.NewReader(mediaReqPayload))
	if err != nil {
		return nil, fmt.Errorf("mapple media session request: %w", err)
	}
	mediaReq.Header.Set("User-Agent", config.DesktopUserAgent)
	mediaReq.Header.Set("Referer", watchURL)
	mediaReq.Header.Set("Content-Type", "application/json")
	setBrowserFetchHeaders(mediaReq, c.siteBase)

	mediaResp, err := sessionClient.Do(mediaReq)
	if err != nil {
		return nil, fmt.Errorf("mapple media session call: %w", err)
	}
	defer mediaResp.Body.Close()
	if mediaResp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(mediaResp.Body)
		return nil, fmt.Errorf("mapple media session status %d: %s", mediaResp.StatusCode, string(respBody))
	}

	var mediaSess mediaSessionResponse
	if err := json.NewDecoder(mediaResp.Body).Decode(&mediaSess); err != nil {
		return nil, fmt.Errorf("mapple decode media session: %w", err)
	}
	streamToken := mediaSess.Token
	if mediaSess.RequiresPow && mediaSess.Pow.Challenge != "" {
		nonce := solvePow(ctx, mediaSess.Pow.Challenge, mediaSess.Pow.Difficulty)
		if nonce == "" {
			return nil, fmt.Errorf("mapple pow solve aborted: %w", ctx.Err())
		}

		powPayload, _ := json.Marshal(map[string]any{
			"mediaId":      tmdbID,
			"mediaType":    mediaType,
			"tv_slug":      tvSlug,
			"requestToken": session.Token,
			"pow": map[string]string{
				"challengeId": mediaSess.Pow.ChallengeID,
				"nonce":       nonce,
			},
		})

		powReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.siteBase+"/api/media-session", bytes.NewReader(powPayload))
		if err != nil {
			return nil, fmt.Errorf("mapple pow request: %w", err)
		}
		powReq.Header.Set("User-Agent", config.DesktopUserAgent)
		powReq.Header.Set("Referer", watchURL)
		powReq.Header.Set("Content-Type", "application/json")
		setBrowserFetchHeaders(powReq, c.siteBase)

		powResp, err := sessionClient.Do(powReq)
		if err != nil {
			return nil, fmt.Errorf("mapple pow call: %w", err)
		}
		defer powResp.Body.Close()

		var powTokenResp struct {
			Success bool   `json:"success"`
			Token   string `json:"token"`
		}
		if err := json.NewDecoder(powResp.Body).Decode(&powTokenResp); err != nil || powTokenResp.Token == "" {
			return nil, fmt.Errorf("mapple pow validation failed: %w", err)
		}
		streamToken = powTokenResp.Token
	}

	if streamToken == "" {
		return nil, fmt.Errorf("mapple missing stream token")
	}

	sourceIDs := []string{"s2", "s25"}
	type laneResult struct {
		sources []provider.MediaSource
		err     error
	}
	laneResults := make([]laneResult, len(sourceIDs))
	var lanes sync.WaitGroup
	for i, sourceID := range sourceIDs {
		lanes.Add(1)
		go func(i int, sourceID string) {
			defer lanes.Done()
			masterURL, sourceLabel, laneErr := c.resolveEncryptedSource(ctx, sessionClient, watchURL,
				tmdbID, mediaType, tvSlug, sourceID, session.Token, streamToken)
			if laneErr != nil {
				laneResults[i].err = laneErr
				return
			}
			if masterURL == "" {
				return
			}
			laneSources, laneErr := c.fetchMasterSources(ctx, sessionClient, masterURL, sourceLabel)
			laneResults[i] = laneResult{sources: laneSources, err: laneErr}
		}(i, sourceID)
	}
	lanes.Wait()

	var (
		sources []provider.MediaSource
		seen    = make(map[string]struct{})
		lastErr = provider.ErrNoSources
	)
	for _, result := range laneResults {
		if result.err != nil {
			lastErr = result.err
			continue
		}
		for _, src := range result.sources {
			if _, ok := seen[src.URL]; ok {
				continue
			}
			seen[src.URL] = struct{}{}
			sources = append(sources, src)
		}
	}
	if len(sources) == 0 {
		return nil, fmt.Errorf("mapple no stream source resolved: %w", lastErr)
	}
	return sources, nil
}

// fetchMasterSources loads a lane's master playlist and expands it into one
// source per advertised variant, falling back to a single quality-probed
// source when the master carries no variant list.
func (c *Client) fetchMasterSources(ctx context.Context, sessionClient *http.Client, masterURL, sourceLabel string) ([]provider.MediaSource, error) {
	masterReq, err := http.NewRequestWithContext(ctx, http.MethodGet, masterURL, nil)
	if err != nil {
		return nil, fmt.Errorf("mapple master request: %w", err)
	}
	masterReq.Header.Set("User-Agent", config.DesktopUserAgent)
	masterReq.Header.Set("Referer", c.siteBase+"/")

	masterHTTPResp, err := sessionClient.Do(masterReq)
	if err != nil {
		return nil, fmt.Errorf("mapple master call: %w", err)
	}
	defer masterHTTPResp.Body.Close()

	if masterHTTPResp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("mapple master playlist status %d", masterHTTPResp.StatusCode)
	}

	variants, err := parseHLSVariants(masterHTTPResp.Body, masterURL)
	if err == nil && len(variants) > 0 {
		sources := make([]provider.MediaSource, 0, len(variants))
		for _, v := range variants {
			sources = append(sources, provider.MediaSource{
				URL:            v.URL,
				Quality:        v.Quality,
				Backend:        sourceLabel,
				Type:           provider.SourceTypeHLS,
				Referer:        c.siteBase + "/",
				UserAgent:      config.DesktopUserAgent,
				SuppressOrigin: false,
			})
		}
		return sources, nil
	}

	quality := kit.DetectHLSQuality(ctx, sessionClient, masterURL, map[string]string{
		"Referer": c.siteBase + "/",
	})
	if quality == "auto" {
		quality = "1080p"
	}

	return []provider.MediaSource{
		{
			URL:            masterURL,
			Quality:        quality,
			Backend:        sourceLabel,
			Type:           provider.SourceTypeHLS,
			Referer:        c.siteBase + "/",
			UserAgent:      config.DesktopUserAgent,
			SuppressOrigin: false,
		},
	}, nil
}

// syncSession performs the page's post-bootstrap tracking call, which binds
// the request token to a device session. The page fires this immediately
// after obtaining the token; media-session rejects tokens that were never
// bound to a device.
func (c *Client) syncSession(ctx context.Context, sessionClient *http.Client, watchURL string) error {
	payload, _ := json.Marshal(map[string]any{
		"type": "track",
		"payload": map[string]any{
			"name": "screen_view",
			"properties": map[string]string{
				"__referrer": "",
				"__path":     watchURL,
				"__title":    "Movie | Mapple",
			},
		},
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.siteBase+"/api/ui/sync/track", bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("mapple sync request: %w", err)
	}
	req.Header.Set("User-Agent", config.DesktopUserAgent)
	req.Header.Set("Referer", watchURL)
	req.Header.Set("Content-Type", "application/json")
	setBrowserFetchHeaders(req, c.siteBase)

	resp, err := sessionClient.Do(req)
	if err != nil {
		return fmt.Errorf("mapple sync call: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("mapple sync status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return nil
}

// resolveEncryptedSource asks the gateway to encrypt one lane's parameters,
// then resolves that lane to its master HLS URL. The upstream labels lanes
// with short source IDs (s2, s25, …); callers try them in order.
func (c *Client) resolveEncryptedSource(
	ctx context.Context,
	sessionClient *http.Client,
	watchURL string,
	tmdbID int,
	mediaType, tvSlug, sourceID, requestToken, streamToken string,
) (masterURL, backend string, err error) {
	encPayload, _ := json.Marshal(map[string]any{
		"data": map[string]any{
			"mediaId":   tmdbID,
			"mediaType": mediaType,
			"tv_slug":   tvSlug,
			"source":    sourceID,
		},
		"endpoint":     "stream-encrypted",
		"requestToken": requestToken,
	})

	encReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.siteBase+"/api/encrypt", bytes.NewReader(encPayload))
	if err != nil {
		return "", "", fmt.Errorf("mapple encrypt request: %w", err)
	}
	encReq.Header.Set("User-Agent", config.DesktopUserAgent)
	encReq.Header.Set("Referer", watchURL)
	encReq.Header.Set("Content-Type", "application/json")
	setBrowserFetchHeaders(encReq, c.siteBase)

	encResp, err := sessionClient.Do(encReq)
	if err != nil {
		return "", "", fmt.Errorf("mapple encrypt call: %w", err)
	}
	defer encResp.Body.Close()

	encBody, _ := io.ReadAll(io.LimitReader(encResp.Body, 1<<20))
	if encResp.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("mapple encrypt (%s) status %d: %s", sourceID, encResp.StatusCode, snippet(encBody))
	}
	var encData encryptResponse
	if err := json.Unmarshal(encBody, &encData); err != nil || encData.URL == "" {
		return "", "", fmt.Errorf("mapple encrypt (%s) response invalid: %s", sourceID, snippet(encBody))
	}

	u, err := url.Parse(encData.URL)
	if err != nil {
		return "", "", fmt.Errorf("mapple parse stream query url: %w", err)
	}
	if !u.IsAbs() {
		baseParsed, err := url.Parse(c.siteBase)
		if err != nil {
			return "", "", fmt.Errorf("mapple parse siteBase: %w", err)
		}
		u = baseParsed.ResolveReference(u)
	}
	q := u.Query()
	q.Set("requestToken", requestToken)
	q.Set("token", streamToken)
	u.RawQuery = q.Encode()

	streamReq, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return "", "", fmt.Errorf("mapple stream resolve request: %w", err)
	}
	streamReq.Header.Set("User-Agent", config.DesktopUserAgent)
	streamReq.Header.Set("Referer", watchURL)
	setBrowserFetchHeaders(streamReq, c.siteBase)

	streamResp, err := sessionClient.Do(streamReq)
	if err != nil {
		return "", "", fmt.Errorf("mapple stream resolve call: %w", err)
	}
	defer streamResp.Body.Close()

	streamBody, _ := io.ReadAll(io.LimitReader(streamResp.Body, 1<<20))
	var sResp streamResponse
	if err := json.Unmarshal(streamBody, &sResp); err != nil || sResp.Data.StreamURL == "" {
		return "", "", fmt.Errorf("mapple stream (%s) status %d missing URL: %s", sourceID, streamResp.StatusCode, snippet(streamBody))
	}
	return strings.TrimSpace(sResp.Data.StreamURL), strings.TrimSpace(sResp.Data.Source), nil
}

// snippet trims an upstream response for a redacted error message.
func snippet(body []byte) string {
	text := strings.TrimSpace(string(body))
	if len(text) > 300 {
		return text[:300]
	}
	return text
}

func (c *Client) fetchSubtitles(ctx context.Context, sessionClient *http.Client, p subtitleFetchParams) ([]provider.SubtitleOption, error) {
	var bodyPayload []byte
	if p.mediaType == provider.MediaTypeTV {
		bodyPayload, _ = json.Marshal([]map[string]any{
			{
				"mediaId":   p.tmdbID,
				"mediaType": "tv",
				"season":    strconv.Itoa(p.season),
				"episode":   strconv.Itoa(p.epNum),
			},
		})
	} else {
		bodyPayload, _ = json.Marshal([]map[string]any{
			{
				"mediaId":   p.tmdbID,
				"mediaType": "movie",
			},
		})
	}

	targetURL := fmt.Sprintf("%s/%s", c.siteBase, p.watchPath)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, targetURL, bytes.NewReader(bodyPayload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", config.DesktopUserAgent)
	req.Header.Set("Referer", targetURL)
	req.Header.Set("Next-Action", p.actionID)
	req.Header.Set("Content-Type", "text/plain;charset=UTF-8")
	setBrowserFetchHeaders(req, c.siteBase)

	resp, err := sessionClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("mapple subtitle action status %d", resp.StatusCode)
	}

	scanner := bufio.NewScanner(resp.Body)
	var tracks []subtitleTrack
	for scanner.Scan() {
		line := scanner.Text()
		// Next.js server actions return chunks formatted as "1:[{...}]"
		colonIdx := strings.Index(line, ":")
		if colonIdx == -1 {
			continue
		}
		jsonPart := line[colonIdx+1:]
		if strings.HasPrefix(strings.TrimSpace(jsonPart), "[") {
			var parsed []subtitleTrack
			if err := json.Unmarshal([]byte(jsonPart), &parsed); err == nil && len(parsed) > 0 {
				tracks = parsed
				break
			}
		}
	}
	if err := scanner.Err(); err != nil && len(tracks) == 0 {
		return nil, fmt.Errorf("mapple read subtitle response: %w", err)
	}

	seen := make(map[string]bool)
	var options []provider.SubtitleOption
	for _, t := range tracks {
		normalizedLang := lang.Normalize(t.Lang)
		if normalizedLang == "" {
			normalizedLang = t.Lang
		}
		if seen[normalizedLang] {
			continue
		}
		seen[normalizedLang] = true

		options = append(options, provider.SubtitleOption{
			URL:      t.URL,
			Language: normalizedLang,
			Default:  normalizedLang == "en",
			Referer:  c.siteBase + "/",
		})
	}

	return options, nil
}

func parseHLSVariants(r io.Reader, baseURL string) ([]hlsVariant, error) {
	scanner := bufio.NewScanner(r)
	var (
		variants    []hlsVariant
		lastWidth   int
		lastHeight  int
		lastBandw   int
		inStreamInf bool
	)

	baseParsed, _ := url.Parse(baseURL)

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		if strings.HasPrefix(line, "#EXT-X-STREAM-INF:") {
			inStreamInf = true
			lastWidth, lastHeight, lastBandw = 0, 0, 0

			if m := reResolution.FindStringSubmatch(line); len(m) == 3 {
				lastWidth, _ = strconv.Atoi(m[1])
				lastHeight, _ = strconv.Atoi(m[2])
			}
			if m := reBandwidth.FindStringSubmatch(line); len(m) == 2 {
				lastBandw, _ = strconv.Atoi(m[1])
			}
			continue
		}

		if inStreamInf && !strings.HasPrefix(line, "#") {
			inStreamInf = false
			dim := max(lastWidth, lastHeight)
			quality := "1080p"
			switch {
			case dim >= 3800:
				quality = "2160p"
			case dim >= 2500:
				quality = "1440p"
			case dim >= 1700:
				quality = "1080p"
			case dim >= 1100:
				quality = "720p"
			case dim >= 700:
				quality = "480p"
			case dim > 0:
				quality = "360p"
			}

			urlStr := line
			if baseParsed != nil {
				if refParsed, err := url.Parse(line); err == nil {
					urlStr = baseParsed.ResolveReference(refParsed).String()
				}
			}

			variants = append(variants, hlsVariant{
				URL:       urlStr,
				Quality:   quality,
				Bandwidth: lastBandw,
				Height:    lastHeight,
			})
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, err
	}

	sort.Slice(variants, func(i, j int) bool {
		if variants[i].Height != variants[j].Height {
			return variants[i].Height > variants[j].Height
		}
		return variants[i].Bandwidth > variants[j].Bandwidth
	})

	return variants, nil
}
