package atlantic

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"kari/internal/config"
	"kari/internal/lang"
	"kari/internal/logging"
	"kari/internal/provider"
)

// AES-GCM cipher keys and payload prefixes used across Atlantic stream backends.
const (
	heliosKeyHex  = "117c358bcfcaf8fe2cfca57c9d2238a300e1c4de2efb83a5012ba84d8a31f1dd"
	heliosPrefix  = "hl_"
	moscowKeyHex  = "55060a042823f51a94c296894a58a0db15f3baef807155140983083fa799ef47"
	moscowPrefix  = "ms_"
	planetsKeyHex = "d31157f286118352b140326486d7da7fdce1b8f2e30d376ab33af875b7c9a80f"
	planetsPrefix = "pl_"
	vesperaKeyHex = "a2e5b8335228d5a70fc02407fd1493a1bd6e956e6927dcf17738c10b92489aa2"
	vesperaPrefix = "sh_"
)

var (
	// lavfDemuxerArgs forces FFmpeg's lavf HLS demuxer and disables picky
	// extension validation for Atlantic's extensionless CDN segment URLs.
	lavfDemuxerArgs = []string{
		"--demuxer=lavf",
		"--demuxer-lavf-format=hls",
		"--demuxer-lavf-o=extension_picky=0",
	}

	reSubtitleLabelTrim = regexp.MustCompile(`(?i)\s*(?:hi\d*|\d+)$`)
)

// decryptAESGCM decrypts a WebCrypto-standard AES-GCM payload formatted as:
// [prefix][hex-encoded 12-byte IV + ciphertext + 16-byte auth tag].
func decryptAESGCM(keyHex, encVal, prefix string) (string, error) {
	if !strings.HasPrefix(encVal, prefix) {
		return encVal, nil
	}
	rawHex := strings.TrimPrefix(encVal, prefix)
	raw, err := hex.DecodeString(rawHex)
	if err != nil {
		return "", fmt.Errorf("hex decode: %w", err)
	}
	if len(raw) < 28 { // 12-byte IV + 16-byte GCM tag minimum
		return "", fmt.Errorf("payload too short: %d bytes", len(raw))
	}
	key, err := hex.DecodeString(keyHex)
	if err != nil {
		return "", fmt.Errorf("key hex decode: %w", err)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", fmt.Errorf("aes new cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("cipher new gcm: %w", err)
	}

	nonce := raw[:12]
	ciphertext := raw[12:]
	plain, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return "", fmt.Errorf("gcm decrypt: %w", err)
	}
	return string(plain), nil
}

// subtitleCollector safely aggregates subtitle options across multiple
// async subtitle providers (Granite, Natsuki).
type subtitleCollector struct {
	mu   sync.Mutex
	subs []provider.SubtitleOption
	seen map[string]struct{}
}

func newSubtitleCollector() *subtitleCollector {
	return &subtitleCollector{
		seen: make(map[string]struct{}),
	}
}

func (c *subtitleCollector) add(opts []provider.SubtitleOption) {
	if len(opts) == 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, opt := range opts {
		key := opt.URL + ":" + opt.Language
		if _, ok := c.seen[key]; ok {
			continue
		}
		c.seen[key] = struct{}{}
		c.subs = append(c.subs, opt)
	}
}

func (c *subtitleCollector) get() []provider.SubtitleOption {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]provider.SubtitleOption(nil), c.subs...)
}

func (c *Client) newRequest(ctx context.Context, targetURL string) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, targetURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", config.DesktopUserAgent)
	req.Header.Set("Referer", c.siteBase+"/")
	req.Header.Set("Origin", c.siteBase)
	return req, nil
}

// fanoutResolve queries all Atlantic stream and subtitle backends concurrently.
func (c *Client) fanoutResolve(
	ctx context.Context,
	tmdbID int,
	mediaType string,
	season, episode int,
	updates chan<- []provider.MediaSource,
) ([]provider.SubtitleOption, error) {
	collector := newSubtitleCollector()
	var delivered int64

	var seenMu sync.Mutex
	seenURLs := make(map[string]struct{})

	var errMu sync.Mutex
	var firstErr error
	recordErr := func(err error) {
		if err == nil || errors.Is(err, provider.ErrNoSources) {
			return
		}
		errMu.Lock()
		if firstErr == nil {
			firstErr = err
		}
		errMu.Unlock()
	}

	emitSource := func(rawURL, quality string) {
		cleanURL := strings.TrimSpace(rawURL)
		if !provider.IsDirectURL(cleanURL) {
			logging.Debug("atlantic dropping non-direct URL", "quality", quality, "url", cleanURL)
			return
		}
		seenMu.Lock()
		if _, ok := seenURLs[cleanURL]; ok {
			seenMu.Unlock()
			return
		}
		seenURLs[cleanURL] = struct{}{}
		seenMu.Unlock()

		src := provider.MediaSource{
			URL:       cleanURL,
			Quality:   quality,
			Type:      provider.SourceTypeHLS,
			Referer:   c.siteBase + "/",
			UserAgent: config.DesktopUserAgent,
			ExtraArgs: append([]string(nil), lavfDemuxerArgs...),
			Subtitles: collector.get(),
		}

		select {
		case updates <- []provider.MediaSource{src}:
			atomic.AddInt64(&delivered, 1)
		case <-ctx.Done():
		}
	}

	var wg sync.WaitGroup

	// 1. Subtitles: Granite API
	wg.Add(1)
	go func() {
		defer wg.Done()
		subs, err := c.fetchGraniteSubtitles(ctx, tmdbID, mediaType, season, episode)
		if err != nil {
			logging.Debug("atlantic granite subtitles failed", "tmdb", tmdbID, "err", err)
			return
		}
		collector.add(subs)
	}()

	// 2. Subtitles: Natsuki API
	wg.Add(1)
	go func() {
		defer wg.Done()
		subs, err := c.fetchNatsukiSubtitles(ctx, tmdbID, mediaType, season, episode)
		if err != nil {
			logging.Debug("atlantic natsuki subtitles failed", "tmdb", tmdbID, "err", err)
			return
		}
		collector.add(subs)
	}()

	// 3. Shard: Helios
	wg.Add(1)
	go func() {
		defer wg.Done()
		streamURL, err := c.fetchHelios(ctx, tmdbID, mediaType, season, episode)
		if err != nil {
			logging.Debug("atlantic helios failed", "tmdb", tmdbID, "err", err)
			recordErr(err)
			return
		}
		if streamURL != "" {
			emitSource(streamURL, "1080p (Helios)")
		}
	}()

	// 4. Shard: Moscow
	wg.Add(1)
	go func() {
		defer wg.Done()
		streamURL, err := c.fetchMoscow(ctx, tmdbID, mediaType, season, episode)
		if err != nil {
			logging.Debug("atlantic moscow failed", "tmdb", tmdbID, "err", err)
			recordErr(err)
			return
		}
		if streamURL != "" {
			emitSource(streamURL, "1080p (Moscow)")
		}
	}()

	// 5. Shards: Planets (Saturn, Neptune)
	wg.Add(1)
	go func() {
		defer wg.Done()
		sources, err := c.fetchPlanets(ctx, tmdbID, mediaType, season, episode)
		if err != nil {
			logging.Debug("atlantic planets failed", "tmdb", tmdbID, "err", err)
			recordErr(err)
			return
		}
		for name, u := range sources {
			emitSource(u, "1080p ("+name+")")
		}
	}()

	// 6. Shard: Athena
	wg.Add(1)
	go func() {
		defer wg.Done()
		streamURL, err := c.fetchAthena(ctx, tmdbID, mediaType, season, episode)
		if err != nil {
			logging.Debug("atlantic athena failed", "tmdb", tmdbID, "err", err)
			recordErr(err)
			return
		}
		if streamURL != "" {
			emitSource(streamURL, "1080p (Athena)")
		}
	}()

	// 7. Shard: Vespera
	wg.Add(1)
	go func() {
		defer wg.Done()
		sources, err := c.fetchVespera(ctx, tmdbID, mediaType, season, episode)
		if err != nil {
			logging.Debug("atlantic vespera failed", "tmdb", tmdbID, "err", err)
			recordErr(err)
			return
		}
		for _, v := range sources {
			emitSource(v.url, v.quality+" (Vespera)")
		}
	}()

	wg.Wait()

	subs := collector.get()
	if atomic.LoadInt64(&delivered) == 0 {
		if ctx.Err() != nil {
			return subs, ctx.Err()
		}
		errMu.Lock()
		defer errMu.Unlock()
		if firstErr != nil {
			return subs, firstErr
		}
		return subs, provider.ErrNoSources
	}
	return subs, nil
}

// streamShardsPayload maps shard endpoints returning { "sources": { "Name": { "url": "..." } } }.
type streamShardsPayload struct {
	Sources map[string]struct {
		URL    string `json:"url"`
		Server string `json:"server"`
		Type   string `json:"type"`
	} `json:"sources"`
}

func (c *Client) fetchHelios(
	ctx context.Context,
	tmdbID int,
	mediaType string,
	season, episode int,
) (string, error) {
	reqCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	params := url.Values{}
	params.Set("tmdbId", strconv.Itoa(tmdbID))
	params.Set("type", mediaType)
	if mediaType == provider.MediaTypeTV {
		params.Set("seasonId", strconv.Itoa(season))
		params.Set("episodeId", strconv.Itoa(episode))
	}

	target := fmt.Sprintf("%s/helios?%s", c.streamBase, params.Encode())
	req, err := c.newRequest(reqCtx, target)
	if err != nil {
		return "", err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", &provider.HTTPError{Code: resp.StatusCode, URL: target}
	}

	var data streamShardsPayload
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return "", fmt.Errorf("helios decode: %w", err)
	}

	item, ok := data.Sources["Moscow"]
	if !ok || strings.TrimSpace(item.URL) == "" {
		return "", provider.ErrNoSources
	}
	return decryptAESGCM(heliosKeyHex, item.URL, heliosPrefix)
}

func (c *Client) fetchMoscow(
	ctx context.Context,
	tmdbID int,
	mediaType string,
	season, episode int,
) (string, error) {
	reqCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	params := url.Values{}
	params.Set("tmdbId", strconv.Itoa(tmdbID))
	params.Set("type", mediaType)
	if mediaType == provider.MediaTypeTV {
		params.Set("seasonId", strconv.Itoa(season))
		params.Set("episodeId", strconv.Itoa(episode))
	}

	target := fmt.Sprintf("%s/moscow?%s", c.streamBase, params.Encode())
	req, err := c.newRequest(reqCtx, target)
	if err != nil {
		return "", err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", &provider.HTTPError{Code: resp.StatusCode, URL: target}
	}

	var data streamShardsPayload
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return "", fmt.Errorf("moscow decode: %w", err)
	}

	item, ok := data.Sources["Moscow"]
	if !ok || strings.TrimSpace(item.URL) == "" {
		return "", provider.ErrNoSources
	}
	return decryptAESGCM(moscowKeyHex, item.URL, moscowPrefix)
}

func (c *Client) fetchPlanets(
	ctx context.Context,
	tmdbID int,
	mediaType string,
	season, episode int,
) (map[string]string, error) {
	reqCtx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()

	params := url.Values{}
	params.Set("tmdbId", strconv.Itoa(tmdbID))
	params.Set("type", mediaType)
	if mediaType == provider.MediaTypeTV {
		params.Set("seasonId", strconv.Itoa(season))
		params.Set("episodeId", strconv.Itoa(episode))
	}

	target := fmt.Sprintf("%s/planets?%s", c.streamBase, params.Encode())
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

	var data streamShardsPayload
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, fmt.Errorf("planets decode: %w", err)
	}

	out := make(map[string]string)
	for _, planet := range []string{"Saturn", "Neptune"} {
		item, ok := data.Sources[planet]
		if !ok || strings.TrimSpace(item.URL) == "" {
			continue
		}
		decrypted, err := decryptAESGCM(planetsKeyHex, item.URL, planetsPrefix)
		if err != nil {
			logging.Debug("atlantic planets decrypt failed", "planet", planet, "err", err)
			continue
		}
		if decrypted == "" {
			continue
		}
		out[planet] = decrypted
	}
	if len(out) == 0 {
		return nil, provider.ErrNoSources
	}
	return out, nil
}

type athenaPayload struct {
	Ready bool   `json:"ready"`
	URL   string `json:"url"`
}

func (c *Client) fetchAthena(
	ctx context.Context,
	tmdbID int,
	mediaType string,
	season, episode int,
) (string, error) {
	reqCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	params := url.Values{}
	params.Set("tmdb", strconv.Itoa(tmdbID))
	params.Set("type", mediaType)
	if mediaType == provider.MediaTypeTV {
		params.Set("season", strconv.Itoa(season))
		params.Set("episode", strconv.Itoa(episode))
	}

	target := fmt.Sprintf("%s/cdn?%s", c.athenaBase, params.Encode())
	req, err := c.newRequest(reqCtx, target)
	if err != nil {
		return "", err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", &provider.HTTPError{Code: resp.StatusCode, URL: target}
	}

	var data athenaPayload
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return "", fmt.Errorf("athena decode: %w", err)
	}
	if !data.Ready || strings.TrimSpace(data.URL) == "" {
		return "", provider.ErrNoSources
	}
	return strings.TrimSpace(data.URL), nil
}

type vesperaPayload struct {
	Qualities []struct {
		Quality string `json:"quality"`
		URL     string `json:"url"`
	} `json:"qualities"`
}

type vesperaStream struct {
	quality string
	url     string
}

func (c *Client) fetchVespera(
	ctx context.Context,
	tmdbID int,
	mediaType string,
	season, episode int,
) ([]vesperaStream, error) {
	reqCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	params := url.Values{}
	params.Set("tmdbId", strconv.Itoa(tmdbID))
	params.Set("type", mediaType)
	if mediaType == provider.MediaTypeTV {
		params.Set("seasonId", strconv.Itoa(season))
		params.Set("episodeId", strconv.Itoa(episode))
	}

	target := fmt.Sprintf("%s/shegu?%s", c.streamBase, params.Encode())
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

	var data vesperaPayload
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, fmt.Errorf("vespera decode: %w", err)
	}

	var out []vesperaStream
	for _, q := range data.Qualities {
		u := strings.TrimSpace(q.URL)
		if u == "" {
			continue
		}
		decrypted, err := decryptAESGCM(vesperaKeyHex, u, vesperaPrefix)
		if err != nil {
			logging.Debug("atlantic vespera decrypt failed", "quality", q.Quality, "err", err)
			continue
		}
		if decrypted == "" {
			continue
		}
		label := strings.TrimSpace(q.Quality)
		if label == "" {
			label = "1080p"
		}
		out = append(out, vesperaStream{quality: label, url: decrypted})
	}
	if len(out) == 0 {
		return nil, provider.ErrNoSources
	}
	return out, nil
}

func (c *Client) fetchGraniteSubtitles(
	ctx context.Context,
	tmdbID int,
	mediaType string,
	season, episode int,
) ([]provider.SubtitleOption, error) {
	reqCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()

	var target string
	if mediaType == provider.MediaTypeTV {
		target = fmt.Sprintf("%s/v1/tv/%d/%d/%d", c.graniteBase, tmdbID, season, episode)
	} else {
		target = fmt.Sprintf("%s/v1/movie/%d", c.graniteBase, tmdbID)
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
		out = append(out, provider.SubtitleOption{
			URL:      u,
			Language: norm,
		})
	}
	return out, nil
}

func (c *Client) fetchNatsukiSubtitles(
	ctx context.Context,
	tmdbID int,
	mediaType string,
	season, episode int,
) ([]provider.SubtitleOption, error) {
	reqCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()

	params := url.Values{}
	params.Set("tmdbId", strconv.Itoa(tmdbID))
	if mediaType == provider.MediaTypeTV {
		params.Set("season", strconv.Itoa(season))
		params.Set("episode", strconv.Itoa(episode))
	}

	target := fmt.Sprintf("%s/subs?%s", c.subsBase, params.Encode())
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

	var data struct {
		Subtitles []struct {
			URL      string `json:"url"`
			Language string `json:"language"`
			FileName string `json:"fileName"`
		} `json:"subtitles"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, fmt.Errorf("natsuki decode: %w", err)
	}

	var out []provider.SubtitleOption
	for _, item := range data.Subtitles {
		u := strings.TrimSpace(item.URL)
		if u == "" {
			continue
		}
		norm := lang.Normalize(item.Language)
		if norm == "" {
			norm = "und"
		}
		out = append(out, provider.SubtitleOption{
			URL:      u,
			Language: norm,
		})
	}
	return out, nil
}
