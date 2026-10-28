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

const nextActionSubtitles = "40b40d9f0a6d65ec7ecc1fe5d977b0df335f81263c"

var (
	reResolution = regexp.MustCompile(`(?i)\bRESOLUTION=(\d+)x(\d+)\b`)
	reBandwidth  = regexp.MustCompile(`(?i)\bBANDWIDTH=(\d+)\b`)
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

	var (
		wg        sync.WaitGroup
		subtitles []provider.SubtitleOption
	)

	wg.Add(1)
	go func() {
		defer wg.Done()
		var err error
		subtitles, err = c.fetchSubtitles(ctx, &sessionClient, watchPath, tmdbID, mediaType, season, epNum)
		if err != nil {
			logging.Debug("mapple subtitles failed", "tmdb", tmdbID, "err", err)
		}
	}()

	sources, err := c.resolveStreamURL(ctx, &sessionClient, watchPath, tmdbID, mediaType, tvSlug)
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
	watchPath string,
	tmdbID int,
	mediaType string,
	tvSlug string,
) ([]provider.MediaSource, error) {
	watchURL := fmt.Sprintf("%s/%s", c.siteBase, watchPath)

	// Step 1: Visit watch page to establish session cookies
	initReq, err := http.NewRequestWithContext(ctx, http.MethodGet, watchURL, nil)
	if err != nil {
		return nil, fmt.Errorf("mapple init request: %w", err)
	}
	initReq.Header.Set("User-Agent", config.DesktopUserAgent)
	initResp, err := sessionClient.Do(initReq)
	if err != nil {
		return nil, fmt.Errorf("mapple init visit: %w", err)
	}
	initResp.Body.Close()

	// Step 2: Request UI session token
	sessReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.siteBase+"/api/ui/session", strings.NewReader("{}"))
	if err != nil {
		return nil, fmt.Errorf("mapple session request: %w", err)
	}
	sessReq.Header.Set("User-Agent", config.DesktopUserAgent)
	sessReq.Header.Set("Origin", c.siteBase)
	sessReq.Header.Set("Referer", watchURL)
	sessReq.Header.Set("Content-Type", "application/json")

	sessResp, err := sessionClient.Do(sessReq)
	if err != nil {
		return nil, fmt.Errorf("mapple session call: %w", err)
	}
	defer sessResp.Body.Close()

	if sessResp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("mapple session status %d", sessResp.StatusCode)
	}

	var session sessionResponse
	if err := json.NewDecoder(sessResp.Body).Decode(&session); err != nil || session.Token == "" {
		return nil, fmt.Errorf("mapple invalid session token: %w", err)
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
	mediaReq.Header.Set("Origin", c.siteBase)
	mediaReq.Header.Set("Referer", watchURL)
	mediaReq.Header.Set("Content-Type", "application/json")

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
		powReq.Header.Set("Origin", c.siteBase)
		powReq.Header.Set("Referer", watchURL)
		powReq.Header.Set("Content-Type", "application/json")

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

	// Step 4: Encrypt endpoint parameters
	encPayload, _ := json.Marshal(map[string]any{
		"data": map[string]any{
			"mediaId":   tmdbID,
			"mediaType": mediaType,
			"tv_slug":   tvSlug,
			"source":    "mapple",
		},
		"endpoint":     "stream-encrypted",
		"requestToken": session.Token,
	})

	encReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.siteBase+"/api/encrypt", bytes.NewReader(encPayload))
	if err != nil {
		return nil, fmt.Errorf("mapple encrypt request: %w", err)
	}
	encReq.Header.Set("User-Agent", config.DesktopUserAgent)
	encReq.Header.Set("Origin", c.siteBase)
	encReq.Header.Set("Referer", watchURL)
	encReq.Header.Set("Content-Type", "application/json")

	encResp, err := sessionClient.Do(encReq)
	if err != nil {
		return nil, fmt.Errorf("mapple encrypt call: %w", err)
	}
	defer encResp.Body.Close()

	var encData encryptResponse
	if err := json.NewDecoder(encResp.Body).Decode(&encData); err != nil || encData.URL == "" {
		return nil, fmt.Errorf("mapple encrypt response invalid: %w", err)
	}

	// Step 5: Resolve final stream URL
	u, err := url.Parse(encData.URL)
	if err != nil {
		return nil, fmt.Errorf("mapple parse stream query url: %w", err)
	}
	if !u.IsAbs() {
		baseParsed, err := url.Parse(c.siteBase)
		if err != nil {
			return nil, fmt.Errorf("mapple parse siteBase: %w", err)
		}
		u = baseParsed.ResolveReference(u)
	}
	q := u.Query()
	q.Set("requestToken", session.Token)
	q.Set("token", streamToken)
	u.RawQuery = q.Encode()

	streamReq, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("mapple stream resolve request: %w", err)
	}
	streamReq.Header.Set("User-Agent", config.DesktopUserAgent)
	streamReq.Header.Set("Origin", c.siteBase)
	streamReq.Header.Set("Referer", watchURL)

	streamHTTPResp, err := sessionClient.Do(streamReq)
	if err != nil {
		return nil, fmt.Errorf("mapple stream resolve call: %w", err)
	}
	defer streamHTTPResp.Body.Close()

	var sResp streamResponse
	if err := json.NewDecoder(streamHTTPResp.Body).Decode(&sResp); err != nil || sResp.Data.StreamURL == "" {
		return nil, fmt.Errorf("mapple stream URL missing in response: %w", err)
	}

	masterURL := sResp.Data.StreamURL

	// Step 6: Parse variants from master playlist
	masterReq, err := http.NewRequestWithContext(ctx, http.MethodGet, masterURL, nil)
	if err != nil {
		return nil, fmt.Errorf("mapple master request: %w", err)
	}
	masterReq.Header.Set("User-Agent", config.DesktopUserAgent)
	masterReq.Header.Set("Referer", c.siteBase+"/")
	masterReq.Header.Set("Origin", c.siteBase)

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
		var sources []provider.MediaSource
		for _, v := range variants {
			sources = append(sources, provider.MediaSource{
				URL:            v.URL,
				Quality:        v.Quality,
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
			Type:           provider.SourceTypeHLS,
			Referer:        c.siteBase + "/",
			UserAgent:      config.DesktopUserAgent,
			SuppressOrigin: false,
		},
	}, nil
}

func (c *Client) fetchSubtitles(
	ctx context.Context,
	sessionClient *http.Client,
	watchPath string,
	tmdbID int,
	mediaType string,
	season, epNum int,
) ([]provider.SubtitleOption, error) {
	var bodyPayload []byte
	if mediaType == provider.MediaTypeTV {
		bodyPayload, _ = json.Marshal([]map[string]any{
			{
				"mediaId":   tmdbID,
				"mediaType": "tv",
				"season":    strconv.Itoa(season),
				"episode":   strconv.Itoa(epNum),
			},
		})
	} else {
		bodyPayload, _ = json.Marshal([]map[string]any{
			{
				"mediaId":   tmdbID,
				"mediaType": "movie",
			},
		})
	}

	targetURL := fmt.Sprintf("%s/%s", c.siteBase, watchPath)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, targetURL, bytes.NewReader(bodyPayload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", config.DesktopUserAgent)
	req.Header.Set("Referer", targetURL)
	req.Header.Set("Next-Action", nextActionSubtitles)
	req.Header.Set("Content-Type", "text/plain;charset=UTF-8")

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
