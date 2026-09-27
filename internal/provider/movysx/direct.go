package movysx

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/bits"
	"net/http"
	"net/url"
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

// Direct resolution talks to the Movy.sx source infrastructure itself
// (api.wecollege.net), ported from temp/movy/server.mjs: a seed is fetched
// per title, eleven city endpoints return XOR-encrypted payloads, and the
// merged sources are direct CDN URLs playable by mpv. No intermediary API
// or proxy is involved.

// defaultStreamBase is the source service backing direct resolution.
const defaultStreamBase = "https://api.wecollege.net"

// sourceEndpoints are the city shards serving encrypted source payloads.
var sourceEndpoints = []string{
	"miami", "boise", "seattle", "denver", "atlanta", "phoenix",
	"portland", "austin", "dallas", "tampa", "orlando",
}

const (
	requestTimeoutMS = 12000
	softTimeoutMS    = 2500
	sourceGraceMS    = 800
	sourceQuorum     = 3
	seedTTLMS        = 30000
)

// --- Stream cipher (ported from temp/movy/server.mjs) ---
//
// The cipher is a byte generator: an FNV-style hash seeds a 61-slot
// table, and each output word is drawn from a rotating accumulator.
// All arithmetic is uint32 with wraparound, matching JS Math.imul/>>>0.
// Two helpers from the JS (bf/Sf parity checks) are provably constant for
// integer inputs — n*(n+1) is always even — so only the live branch is
// implemented here.

const (
	cipherSlots  = 61
	cipherMS     = 2654435769
	cipherAccXor = 2779096485
)

// cipherMagic prefixes every valid plaintext ("mvm1").
var cipherMagic = []byte{109, 118, 109, 49}

// mixHash is the JS ui() avalanche mixer.
func mixHash(l uint32) uint32 {
	l ^= l >> 16
	l *= 2246822507
	l ^= l >> 13
	l *= 3266489909
	l ^= l >> 16
	return l
}

// fnvSeed is the JS wf() seed hash.
func fnvSeed(s string) uint32 {
	o := uint32(2166136261)
	for i := 0; i < len(s); i++ {
		o = (o ^ uint32(s[i])) * 16777619
	}
	return mixHash(o)
}

// cipherState is the JS {S, acc} generator state. Slots start empty;
// present tracks which of the 61 positions have been written.
type cipherState struct {
	slots   [cipherSlots]uint32
	present [cipherSlots]bool
	acc     uint32
}

// newCipherState is the JS Nf() initializer.
func newCipherState(seed string, tmdbID uint32) *cipherState {
	st := &cipherState{}
	i := mixHash(fnvSeed(seed) ^ mixHash(tmdbID^cipherMS))
	for r := uint32(0); r < 8; r++ {
		n := i % cipherSlots
		i = bits.RotateLeft32(i+cipherMS, int(7+(r&7)))
		st.slots[n] = i ^ mixHash(i)
		st.present[n] = true
		i = mixHash(i + n)
	}
	st.acc = mixHash(i ^ cipherAccXor)
	return st
}

// next is the JS Rf() word generator; offset counts emitted words.
func (st *cipherState) next(offset uint32) uint32 {
	r := st.acc % cipherSlots
	var neg uint32
	if st.present[r] {
		neg = 0xFFFFFFFF
	}
	u := st.slots[r]
	d := cipherMS * (offset + 1)
	g := (st.acc ^ (u ^ d)) | (st.acc & (u ^ d) & neg)
	g = bits.RotateLeft32(g+st.acc, int(r&31)) ^ bits.RotateLeft32(st.acc, int((r*7)&31))
	st.acc = mixHash(g + cipherMS)
	st.slots[r] = st.acc
	st.present[r] = true
	return st.acc
}

// keystream is the JS Cf() byte generator.
func keystream(seed string, tmdbID uint32, n int) []byte {
	st := newCipherState(seed, tmdbID)
	out := make([]byte, 0, n)
	var offset uint32
	for len(out) < n {
		d := st.next(offset)
		offset++
		for k := 0; k < 4 && len(out) < n; k++ {
			out = append(out, byte(d>>(8*k)))
		}
	}
	return out
}

// decryptPayload is the JS Df() payload decryptor: base64url input, XOR
// with the keystream, magic check, UTF-8 body.
func decryptPayload(enc, seed string, tmdbID uint32) (string, error) {
	if enc == "" {
		return "", fmt.Errorf("movysx decrypt: empty payload")
	}
	if seed == "" {
		return "", fmt.Errorf("movysx decrypt: empty seed")
	}
	b64 := strings.ReplaceAll(strings.ReplaceAll(enc, "-", "+"), "_", "/")
	if pad := len(b64) % 4; pad != 0 {
		b64 += strings.Repeat("=", 4-pad)
	}
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return "", fmt.Errorf("movysx decrypt: decode: %w", err)
	}
	ks := keystream(seed, tmdbID, len(raw))
	for i := range raw {
		raw[i] ^= ks[i]
	}
	for i, b := range cipherMagic {
		if i >= len(raw) || raw[i] != b {
			return "", fmt.Errorf("movysx decrypt: bad seed or tampered payload")
		}
	}
	return string(raw[len(cipherMagic):]), nil
}

// --- Upstream types ---

type upstreamSource struct {
	URL     string `json:"url"`
	File    string `json:"file"`
	Quality string `json:"quality"`
}

type upstreamSubtitle struct {
	URL      string `json:"url"`
	File     string `json:"file"`
	Lang     string `json:"lang"`
	Language string `json:"language"`
	Name     string `json:"name"`
}

type upstreamPayload struct {
	Sources   []upstreamSource   `json:"sources"`
	Subtitles []upstreamSubtitle `json:"subtitles"`
}

type seedResp struct {
	Seed  string `json:"seed"`
	TTLMs int64  `json:"ttlMs"`
}

type tmdbMeta struct {
	Title           string `json:"title"`
	Name            string `json:"name"`
	OriginalTitle   string `json:"original_title"`
	OriginalName    string `json:"original_name"`
	ReleaseDate     string `json:"release_date"`
	FirstAirDate    string `json:"first_air_date"`
	ImdbID          string `json:"imdb_id"`
	NumberOfSeasons int    `json:"number_of_seasons"`
	ExternalIDs     struct {
		ImdbID string `json:"imdb_id"`
	} `json:"external_ids"`
}

func (m tmdbMeta) title() string {
	for _, s := range []string{m.Title, m.Name, m.OriginalTitle, m.OriginalName} {
		if strings.TrimSpace(s) != "" {
			return strings.TrimSpace(s)
		}
	}
	return ""
}

func (m tmdbMeta) year() string {
	for _, d := range []string{m.ReleaseDate, m.FirstAirDate} {
		if len(strings.TrimSpace(d)) >= 4 {
			return strings.TrimSpace(d)[:4]
		}
	}
	return ""
}

func (m tmdbMeta) imdbID() string {
	if strings.TrimSpace(m.ImdbID) != "" {
		return strings.TrimSpace(m.ImdbID)
	}
	return strings.TrimSpace(m.ExternalIDs.ImdbID)
}

// seedEntry is a cached seed with its expiry.
type seedEntry struct {
	seed      string
	expiresAt time.Time
}

// --- Direct client state (held on Client; see client.go) ---

// fetchMeta loads TMDB metadata for a title, rotating keys on auth errors.
func (c *Client) fetchMeta(ctx context.Context, tmdbID int, mediaType string) (*tmdbMeta, error) {
	var lastAuthErr error
	for {
		apiKey, err := c.keyPool.NextKey()
		if err != nil {
			if lastAuthErr != nil {
				return nil, fmt.Errorf("movysx meta auth failed after key rotation: %w", lastAuthErr)
			}
			return nil, err
		}
		target := fmt.Sprintf("%s/%s/%d?append_to_response=external_ids&language=en-US&api_key=%s",
			strings.TrimRight(c.tmdbBase, "/"), mediaType, tmdbID, url.QueryEscape(apiKey))
		meta, err := fetchJSON[tmdbMeta](ctx, c.httpClient, target)
		if err == nil {
			return &meta, nil
		}
		if !isAuthError(err) {
			return nil, err
		}
		c.keyPool.MarkFailed(apiKey)
		lastAuthErr = err
	}
}

// fetchSeed returns the cached source seed or fetches a fresh one.
func (c *Client) fetchSeed(ctx context.Context, tmdbID int) (string, error) {
	now := time.Now()
	if v, ok := c.seedCache.Load(tmdbID); ok {
		if e := v.(seedEntry); e.expiresAt.Add(-5 * time.Second).After(now) {
			return e.seed, nil
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		fmt.Sprintf("%s/seed?mediaId=%d", strings.TrimRight(c.streamBase, "/"), tmdbID), nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", config.DesktopUserAgent)
	req.Header.Set("Referer", config.MovyReferer)
	req.Header.Set("Origin", "https://www.movy.sx")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("movysx seed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", &provider.HTTPError{Code: resp.StatusCode, URL: req.URL.String()}
	}
	body, err := httpclient.ReadCapped(resp)
	if err != nil {
		return "", fmt.Errorf("movysx seed: read body: %w", err)
	}
	var sr seedResp
	if err := json.Unmarshal(body, &sr); err != nil {
		return "", fmt.Errorf("movysx seed: decode: %w", err)
	}
	if strings.TrimSpace(sr.Seed) == "" {
		return "", fmt.Errorf("movysx seed: empty seed")
	}
	ttl := sr.TTLMs
	if ttl <= 0 {
		ttl = seedTTLMS
	}
	c.seedCache.Store(tmdbID, seedEntry{seed: sr.Seed, expiresAt: now.Add(time.Duration(ttl) * time.Millisecond)})
	return sr.Seed, nil
}

// refreshSeed drops the cached seed and fetches a fresh one (after 401s).
func (c *Client) refreshSeed(ctx context.Context, tmdbID int) (string, error) {
	c.seedCache.Delete(tmdbID)
	return c.fetchSeed(ctx, tmdbID)
}

// fetchEndpointSources queries one city shard and decrypts its payload,
// refreshing the seed once on a 401 like the web player does.
func (c *Client) fetchEndpointSources(ctx context.Context, endpoint string, params url.Values, seed string, tmdbID int) (*upstreamPayload, error) {
	target := fmt.Sprintf("%s/%s/sources?%s", strings.TrimRight(c.streamBase, "/"), endpoint, params.Encode())
	attempt := func(s string) (*upstreamPayload, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("User-Agent", config.DesktopUserAgent)
		req.Header.Set("Referer", config.MovyReferer)
		req.Header.Set("Origin", "https://www.movy.sx")
		req.Header.Set("Accept", "application/json, text/plain, */*")

		resp, err := c.httpClient.Do(req)
		if err != nil {
			return nil, &provider.HTTPError{Code: http.StatusBadGateway, URL: target}
		}
		defer resp.Body.Close()
		if resp.StatusCode == http.StatusUnauthorized {
			return nil, &provider.HTTPError{Code: http.StatusUnauthorized, URL: target}
		}
		if resp.StatusCode != http.StatusOK {
			return nil, &provider.HTTPError{Code: resp.StatusCode, URL: target}
		}
		body, err := httpclient.ReadCapped(resp)
		if err != nil {
			return nil, err
		}
		plain, err := decryptPayload(strings.TrimSpace(string(body)), s, uint32(tmdbID))
		if err != nil {
			return nil, err
		}
		var out upstreamPayload
		if err := json.Unmarshal([]byte(plain), &out); err != nil {
			return nil, fmt.Errorf("movysx sources: decode: %w", err)
		}
		return &out, nil
	}

	out, err := attempt(seed)
	if err == nil {
		return out, nil
	}
	// A 401 means the seed rotated: drop the cache and retry once with a
	// fresh seed, mirroring the web player. Anything else stands.
	if !isHTTPErrorCode(err, http.StatusUnauthorized) {
		return nil, err
	}
	fresh, fErr := c.refreshSeed(ctx, tmdbID)
	if fErr != nil {
		return nil, err
	}
	return attempt(fresh)
}

func isHTTPErrorCode(err error, code int) bool {
	httpErr, ok := err.(*provider.HTTPError)
	return ok && httpErr.Code == code
}

// fetchSourcesDirect fans out to every city shard with quorum/grace timing:
// once sourceQuorum shards deliver, stragglers get sourceGraceMS to land;
// the soft deadline fires only while sources are already held.
func (c *Client) fetchSourcesDirect(ctx context.Context, mediaType string, tmdbID, season, episode int, title, year, imdbID string, totalSeasons int, seed string) (*mergedResults, error) {
	params := url.Values{}
	params.Set("title", title)
	params.Set("mediaType", mediaType)
	params.Set("year", year)
	params.Set("tmdbId", fmt.Sprintf("%d", tmdbID))
	params.Set("imdbId", imdbID)
	params.Set("episodeId", fmt.Sprintf("%d", max(episode, 1)))
	params.Set("seasonId", fmt.Sprintf("%d", max(season, 1)))
	params.Set("enc", "2")
	params.Set("seed", seed)
	if totalSeasons > 0 {
		params.Set("totalSeasons", fmt.Sprintf("%d", totalSeasons))
	}

	type shardResult struct {
		payload *upstreamPayload
	}
	results := make(chan shardResult, len(sourceEndpoints))
	var wg sync.WaitGroup
	for _, ep := range sourceEndpoints {
		wg.Add(1)
		go func(endpoint string) {
			defer wg.Done()
			reqCtx, cancel := context.WithTimeout(ctx, requestTimeoutMS*time.Millisecond)
			defer cancel()
			p, err := c.fetchEndpointSources(reqCtx, endpoint, params, seed, tmdbID)
			if err != nil {
				logging.Debug("movysx shard failed", "endpoint", endpoint, "err", err)
				return
			}
			if p == nil {
				return
			}
			results <- shardResult{payload: p}
		}(ep)
	}
	go func() {
		wg.Wait()
		close(results)
	}()

	var collected []*upstreamPayload
	withSources := func() int {
		n := 0
		for _, p := range collected {
			if len(p.Sources) > 0 {
				n++
			}
		}
		return n
	}

	softTimer := time.NewTimer(softTimeoutMS * time.Millisecond)
	defer softTimer.Stop()
	var graceTimer <-chan time.Time
	graceStarted := false

	for {
		select {
		case r, ok := <-results:
			if !ok {
				results = nil
				goto done
			}
			collected = append(collected, r.payload)
			if !graceStarted && withSources() >= sourceQuorum {
				graceStarted = true
				graceTimer = time.After(sourceGraceMS * time.Millisecond)
			}
		case <-graceTimer:
			goto done
		case <-softTimer.C:
			if withSources() > 0 {
				goto done
			}
			// No sources yet: keep waiting for the stragglers.
			softTimer.Reset(softTimeoutMS * time.Millisecond)
		case <-ctx.Done():
			goto done
		}
		if results == nil {
			break
		}
	}
done:
	merged := mergeProviderResults(collected)
	if len(merged.sources) == 0 {
		return nil, provider.ErrNoSources
	}
	return merged, nil
}

// --- Merge + normalize (ported from server.mjs) ---

type mergedSource struct {
	url     string
	quality string
	typ     string
}

type mergedAudio struct {
	url   string
	lang  string
	label string
	typ   string
}

type mergedSubtitle struct {
	url  string
	lang string
	name string
}

type mergedResults struct {
	sources   []mergedSource
	audio     []mergedAudio
	subtitles []mergedSubtitle
}

var qualityRank = map[string]int{
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

// streamContainer infers the container from the URL for player selection.
func streamContainer(u string) string {
	l := strings.ToLower(u)
	switch {
	case strings.Contains(l, ".m3u8"):
		return "hls"
	case strings.Contains(l, ".mpd"):
		return "dash"
	case kit.IsMp4URL(u):
		return "mp4"
	default:
		return "auto"
	}
}

func cleanURL(value string) string {
	return strings.TrimSpace(value)
}

// mergeProviderResults aggregates shard payloads: unique sources
// best-first, dub-labeled rows as audio tracks, real subtitles only.
func mergeProviderResults(payloads []*upstreamPayload) *mergedResults {
	out := &mergedResults{}
	sourceSet := map[string]struct{}{}
	audioSet := map[string]struct{}{}

	for _, data := range payloads {
		if data == nil {
			continue
		}
		for _, s := range data.Sources {
			u := cleanURL(s.URL)
			if u == "" {
				u = cleanURL(s.File)
			}
			if u == "" {
				continue
			}
			if dl := kit.DubLangOf(s.Quality); dl != "" {
				if _, ok := audioSet[u]; ok {
					continue
				}
				audioSet[u] = struct{}{}
				out.audio = append(out.audio, mergedAudio{url: u, lang: dl, label: strings.TrimSpace(s.Quality), typ: streamContainer(u)})
				continue
			}
			if _, ok := sourceSet[u]; ok {
				continue
			}
			sourceSet[u] = struct{}{}
			out.sources = append(out.sources, mergedSource{url: u, quality: normalizeQuality(s.Quality), typ: streamContainer(u)})
		}
	}

	// Best-first: quality rank, then mp4 preferred over HLS at parity.
	sortMergedSources(out.sources)

	subSet := map[string]struct{}{}
	for _, data := range payloads {
		if data == nil {
			continue
		}
		for _, sub := range data.Subtitles {
			u := cleanURL(sub.URL)
			if u == "" {
				u = cleanURL(sub.File)
			}
			if u == "" {
				continue
			}
			if !kit.IsSubtitleURL(u) {
				continue
			}
			if _, ok := subSet[u]; ok {
				continue
			}
			subSet[u] = struct{}{}
			language := lang.Normalize(sub.Lang)
			if language == "" {
				language = lang.Normalize(sub.Language)
			}
			if language == "" {
				language = "und"
			}
			out.subtitles = append(out.subtitles, mergedSubtitle{url: u, lang: language, name: sub.Name})
		}
	}
	return out
}

func sortMergedSources(sources []mergedSource) {
	rankOf := func(q string) int {
		if r, ok := qualityRank[q]; ok {
			return r
		}
		return 99
	}
	for i := 1; i < len(sources); i++ {
		for j := i; j > 0; j-- {
			a, b := sources[j], sources[j-1]
			ra, rb := rankOf(a.quality), rankOf(b.quality)
			swap := ra < rb
			if ra == rb {
				swap = kit.IsMp4URL(a.url) && !kit.IsMp4URL(b.url)
			}
			if !swap {
				break
			}
			sources[j], sources[j-1] = b, a
		}
	}
}

// fetchJSON GETs target and decodes the JSON body.
func fetchJSON[T any](ctx context.Context, hc *http.Client, target string) (T, error) {
	var zero T
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return zero, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", config.DesktopUserAgent)
	resp, err := hc.Do(req)
	if err != nil {
		return zero, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return zero, &provider.HTTPError{Code: resp.StatusCode, URL: target}
	}
	body, err := httpclient.ReadCapped(resp)
	if err != nil {
		return zero, err
	}
	var out T
	if err := json.Unmarshal(body, &out); err != nil {
		return zero, fmt.Errorf("decode response: %w", err)
	}
	return out, nil
}

// isAuthError reports TMDB key auth failures worth rotating over.
func isAuthError(err error) bool {
	httpErr, ok := err.(*provider.HTTPError)
	return ok && (httpErr.Code == http.StatusUnauthorized || httpErr.Code == http.StatusTooManyRequests)
}
