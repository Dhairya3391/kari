package bingr

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
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
	// sealPrefix marks a Bingr sealed source payload.
	sealPrefix = "v1."
	// sealKey derives the XOR stream that scrambles a sealed payload. Bingr's
	// player ships the same key; the payload is obfuscation, not encryption.
	sealKey = "vY7#qL2!mZ9@wK4$"
	// maxWatchPageBytes bounds the HTML read while extracting the payload.
	maxWatchPageBytes = 8 << 20
)

var (
	// sealedPayloadPattern matches the base64url body of a sealed payload
	// inside a watch page's streamed RSC data. Real payloads are tens of
	// kilobytes long; the floor keeps unrelated "v1." strings from matching.
	sealedPayloadPattern = regexp.MustCompile(`v1\.[A-Za-z0-9_-]{100,}`)
	// watchTokenPattern extracts the short-lived source token the watch page
	// gives its player for querying other backend servers.
	watchTokenPattern = regexp.MustCompile(`token\\*":\\*"([A-Za-z0-9_.-]{10,60})`)
)

// sealedSources is the decrypted payload carried by a Bingr watch page.
type sealedSources struct {
	Provider sealedProvider `json:"provider"`
	Streams  []sealedStream `json:"streams"`
	Servers  []sealedServer `json:"servers"`
}

// sealedProvider names the upstream lane that produced the streams.
type sealedProvider struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	Tag   string `json:"tag"`
	HEVC  bool   `json:"hevc"`
}

// sealedServer describes one selectable backend shown by the player.
type sealedServer struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	Tag   string `json:"tag"`
}

// sealedStream is one playable transport variant with its own subtitle set.
type sealedStream struct {
	URL       string           `json:"url"`
	Type      string           `json:"type"`
	Quality   string           `json:"quality"`
	Language  string           `json:"language"`
	Label     string           `json:"label"`
	Subtitles []sealedSubtitle `json:"subtitles"`
}

// sealedSubtitle is a subtitle track attached to a sealed stream.
type sealedSubtitle struct {
	URL   string `json:"url"`
	Lang  string `json:"lang"`
	Label string `json:"label"`
}

// fetchSealedSources loads a movie or TV watch page and decodes its streams.
func (c *Client) fetchSealedSources(
	ctx context.Context,
	mediaType string,
	tmdbID, season, epNum int,
) ([]provider.MediaSource, error) {
	query := url.Values{"kind": {mediaType}, "tmdb": {strconv.Itoa(tmdbID)}}
	if mediaType == provider.MediaTypeTV {
		query.Set("s", strconv.Itoa(season))
		query.Set("e", strconv.Itoa(epNum))
	}
	return c.collectSources(ctx, c.watchURL(mediaType, tmdbID, season, epNum), query)
}

// collectSources loads a watch page, decodes the source payload it embeds for
// its own player, then gathers every backend's streams using the page's token.
func (c *Client) collectSources(
	ctx context.Context,
	watchURL string,
	query url.Values,
) ([]provider.MediaSource, error) {
	reqCtx, cancel := context.WithTimeout(ctx, timeouts.BingrFanout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, watchURL, nil)
	if err != nil {
		return nil, fmt.Errorf("bingr watch request: %w", err)
	}
	req.Header.Set("User-Agent", config.DesktopUserAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("bingr watch page: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, &provider.HTTPError{Code: resp.StatusCode, URL: watchURL}
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxWatchPageBytes))
	if err != nil {
		return nil, fmt.Errorf("bingr watch page read: %w", err)
	}
	sealed := sealedPayloadPattern.Find(body)
	if sealed == nil {
		return nil, fmt.Errorf("bingr watch page has no sealed payload: %w", provider.ErrNoSources)
	}

	payload, err := decodeSealedSources(string(sealed))
	if err != nil {
		return nil, err
	}
	sources := mapSealedSources(payload, c.siteBase+"/")

	// The embedded payload only carries one server's streams, and that server
	// is often stale. The watch page gives its player a token for querying
	// every backend, so gather all of them in one pass and let the service
	// rank the combined transports.
	if match := watchTokenPattern.FindSubmatch(body); match != nil && len(payload.Servers) > 0 {
		all := c.fetchServerSources(ctx, string(match[1]), query, payload.Servers)
		sources = append(sources, all...)
	}
	sources = dedupeSources(sources)
	sources = c.expandHLSMasters(ctx, sources)
	if len(sources) == 0 {
		return nil, provider.ErrNoSources
	}
	logging.Debug("bingr sealed sources", "url", watchURL, "backend", payload.Provider.Label, "streams", len(sources))
	return sources, nil
}

// expandHLSMasters replaces a master playlist with one playable source per
// declared variant. Bingr's Edge lane keeps audio in separate renditions; mpv
// opening the master buffers every rendition before it can start and routinely
// misses the player's startup window. A variant starts almost immediately,
// while the preserved ManifestURL lets the service attach the master's audio
// renditions as selectable inputs.
func (c *Client) expandHLSMasters(ctx context.Context, sources []provider.MediaSource) []provider.MediaSource {
	if len(sources) == 0 {
		return sources
	}
	expanded := make([][]provider.MediaSource, len(sources))
	var wg sync.WaitGroup
	for i, src := range sources {
		if src.Type != provider.SourceTypeHLS || strings.TrimSpace(src.URL) == "" {
			expanded[i] = []provider.MediaSource{src}
			continue
		}
		wg.Add(1)
		go func(i int, src provider.MediaSource) {
			defer wg.Done()
			probeCtx, cancel := context.WithTimeout(ctx, timeouts.BingrShard)
			defer cancel()
			variants, err := kit.ParseHLSVariants(probeCtx, c.httpClient, src.URL, map[string]string{
				"Referer":    src.Referer,
				"User-Agent": src.UserAgent,
			})
			if err != nil || len(variants) == 0 {
				expanded[i] = []provider.MediaSource{src}
				return
			}
			out := make([]provider.MediaSource, 0, len(variants))
			for _, variant := range variants {
				playable := src
				playable.URL = variant.URL
				playable.ManifestURL = src.URL
				playable.Bitrate = variant.Bandwidth
				if height := kit.CredibleHeight(variant.Height, variant.Codecs); height > 0 {
					playable.Height = height
					playable.Quality = kit.QualityLabelFromHeight(height)
				}
				out = append(out, playable)
			}
			expanded[i] = out
		}(i, src)
	}
	wg.Wait()
	var out []provider.MediaSource
	for _, group := range expanded {
		out = append(out, group...)
	}
	return out
}

// fetchServerSources queries every backend listed on the watch page for its
// sealed stream payload. They all run in parallel under a per-server budget;
// a dead or rate-limited server contributes nothing and never blocks the rest.
func (c *Client) fetchServerSources(
	ctx context.Context,
	token string,
	query url.Values,
	servers []sealedServer,
) []provider.MediaSource {
	type result struct {
		sources []provider.MediaSource
	}
	results := make(chan result, len(servers))
	var wg sync.WaitGroup

	for _, srv := range servers {
		if srv.ID == "" {
			continue
		}
		wg.Add(1)
		go func(srv sealedServer) {
			defer wg.Done()
			reqCtx, cancel := context.WithTimeout(ctx, timeouts.BingrShard)
			defer cancel()

			serverQuery := make(url.Values, len(query)+2)
			for key, values := range query {
				serverQuery[key] = append([]string(nil), values...)
			}
			serverQuery.Set("t", token)
			serverQuery.Set("provider", srv.ID)
			endpoint := fmt.Sprintf("%s/api/sources?%s", c.siteBase, serverQuery.Encode())
			req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, endpoint, nil)
			if err != nil {
				return
			}
			req.Header.Set("User-Agent", config.DesktopUserAgent)
			req.Header.Set("Accept", "application/json")
			req.Header.Set("Referer", c.siteBase+"/")

			resp, err := c.httpClient.Do(req)
			if err != nil {
				logging.Debug("bingr alternate server failed", "server", srv.ID, "err", err)
				return
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				return
			}
			body, err := io.ReadAll(io.LimitReader(resp.Body, maxWatchPageBytes))
			if err != nil {
				return
			}
			sealed := sealedPayloadPattern.Find(body)
			if sealed == nil {
				return
			}
			payload, err := decodeSealedSources(string(sealed))
			if err != nil {
				logging.Debug("bingr alternate server seal failed", "server", srv.ID, "err", err)
				return
			}
			results <- result{sources: mapSealedSources(payload, c.siteBase+"/")}
		}(srv)
	}

	wg.Wait()
	close(results)
	var out []provider.MediaSource
	for r := range results {
		out = append(out, r.sources...)
	}
	return out
}

// dedupeSources drops repeated transports, keeping the first occurrence.
func dedupeSources(sources []provider.MediaSource) []provider.MediaSource {
	seen := make(map[string]bool, len(sources))
	out := make([]provider.MediaSource, 0, len(sources))
	for _, s := range sources {
		key := s.TransportIdentity()
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, s)
	}
	return out
}

// watchURL builds the public watch page that owns a title's sealed payload.
func (c *Client) watchURL(mediaType string, tmdbID, season, epNum int) string {
	if mediaType == provider.MediaTypeTV {
		return fmt.Sprintf("%s/watch/tv/%d/%d/%d", c.siteBase, tmdbID, season, epNum)
	}
	return fmt.Sprintf("%s/watch/movie/%d", c.siteBase, tmdbID)
}

// decodeSealedSources unscrambles a v1 payload and parses its JSON body.
func decodeSealedSources(sealed string) (*sealedSources, error) {
	sealed = strings.TrimSpace(sealed)
	if !strings.HasPrefix(sealed, sealPrefix) {
		return nil, fmt.Errorf("bingr seal: bad prefix")
	}
	encoded := strings.TrimRight(sealed[len(sealPrefix):], "=")
	data, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return nil, fmt.Errorf("bingr seal decode: %w", err)
	}
	if len(data) <= 12 {
		return nil, fmt.Errorf("bingr seal: payload too short")
	}

	plain := unscrambleSeal(data[12:], data[:12])
	var payload sealedSources
	if err := json.NewDecoder(bytes.NewReader(plain)).Decode(&payload); err != nil {
		return nil, fmt.Errorf("bingr seal json: %w", err)
	}
	return &payload, nil
}

// unscrambleSeal applies Bingr's byte-wise XOR stream. The cipher is
// symmetric: the site uses the same routine to seal and to open a payload.
func unscrambleSeal(ciphertext, nonce []byte) []byte {
	out := append([]byte(nil), ciphertext...)
	key := []byte(sealKey)
	var seeds [4]uint32
	for i := range seeds {
		h := uint32(0x811c9dc5) ^ (uint32(0x9e3779b9) * uint32(i))
		for _, c := range key {
			h = (h ^ uint32(c)) * 0x1000193
		}
		for _, b := range nonce {
			h = (h ^ uint32(b)) * 0x1000193
		}
		seeds[i] = h
	}

	n, l, a, s := seeds[0], seeds[1], seeds[2], seeds[3]
	var stream uint32
	for i := range out {
		if i&3 == 0 {
			value := n + l
			n = l ^ (l >> 9)
			l = a + (a << 3)
			rotated := (a << 21) | (a >> 11)
			s++
			value += s
			a = rotated + value
			stream = value
		}
		out[i] ^= byte(stream >> ((i & 3) * 8))
	}
	return out
}

// mapSealedSources converts a decrypted payload into typed media sources. The
// stream language describes subtitle dubs as well as audio, so it is left
// unknown rather than guessed as an audio identity.
func mapSealedSources(payload *sealedSources, referer string) []provider.MediaSource {
	backend := payload.Provider.Label
	if backend == "" {
		backend = payload.Provider.ID
	}

	var out []provider.MediaSource
	for _, s := range payload.Streams {
		rawURL := strings.TrimSpace(s.URL)
		if rawURL == "" {
			continue
		}
		quality := strings.TrimSpace(s.Quality)
		if quality == "" {
			quality = "HD"
		}
		srcType := provider.SourceTypeHLS
		if strings.Contains(strings.ToLower(s.Type), "mp4") || strings.HasSuffix(strings.ToLower(rawURL), ".mp4") {
			srcType = provider.SourceTypeMP4
		}

		subs := make([]provider.SubtitleOption, 0, len(s.Subtitles))
		seen := make(map[string]bool, len(s.Subtitles))
		for _, sub := range s.Subtitles {
			subURL := strings.TrimSpace(sub.URL)
			if subURL == "" {
				continue
			}
			language := lang.FromCodeOrLabel(sub.Lang, sub.Label)
			key := language + ":" + subURL
			if seen[key] {
				continue
			}
			seen[key] = true
			subs = append(subs, provider.SubtitleOption{URL: subURL, Language: language})
		}

		src := provider.MediaSource{
			URL:       rawURL,
			Quality:   quality,
			Backend:   backend,
			Type:      srcType,
			Height:    kit.QualityHeight(quality),
			UserAgent: config.DesktopUserAgent,
			Referer:   referer,
			Subtitles: subs,
		}
		// The CDN hotlink-protects the audio and subtitle sub-playlists, so
		// playback needs the site referer. Pointing ManifestURL at the same
		// master also lets the service probe attach its audio renditions.
		if srcType == provider.SourceTypeHLS {
			src.ManifestURL = rawURL
		}
		out = append(out, src)
	}
	return out
}
