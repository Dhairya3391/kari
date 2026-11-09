package provider

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"kari/internal/config"
)

const (
	// liveProbeBodyBytes bounds how much of a live response is read. Playlists
	// are a few KB and a media segment header fits comfortably, so the probe
	// never drains an endless live stream.
	liveProbeBodyBytes = 64 << 10
	// maxHLSSegmentProbes caps how many child playlists are tried before the
	// source is considered dead. Some masters list dozens of dead variants;
	// two healthy candidates prove the manifest has a working lane.
	maxHLSVariantProbes = 3
)

// VerifyLiveStream probes one live source and reports whether it currently
// looks playable. It sends a ranged GET (some IPTV servers reject HEAD) using
// the source's transport headers, accepts any 2xx, and requires HLS responses
// to be valid playlists. Master playlists are followed into a variant and its
// first segment, so a manifest that points only at dead lanes is rejected
// before the player opens it.
//
// A successful probe proves reachability, not that every buffer will arrive;
// callers must treat a probe failure as soft evidence and never use it to hide
// the only available source.
func VerifyLiveStream(ctx context.Context, hc *http.Client, source MediaSource) error {
	rawURL := strings.TrimSpace(source.URL)
	if rawURL == "" {
		return fmt.Errorf("verify live stream: empty URL: %w", ErrNoSources)
	}
	if hc == nil {
		return fmt.Errorf("verify live stream: nil HTTP client")
	}

	probe, err := probeLiveURL(ctx, hc, source, rawURL)
	if err != nil {
		return err
	}
	if !isHLSStream(source, probe.contentType, rawURL) {
		if len(probe.body) == 0 {
			return fmt.Errorf("verify live stream: empty response body")
		}
		return nil
	}
	if !looksLikeHLSPlaylist(probe.body) {
		return fmt.Errorf("verify live stream: response is not an HLS playlist")
	}
	return verifyHLSPlaylist(ctx, hc, source, rawURL, probe.body)
}

// liveProbe is one response fetched while checking a live URL.
type liveProbe struct {
	body        []byte
	contentType string
}

// probeLiveURL fetches a live URL with the headers playback would send and
// reads at most liveProbeBodyBytes.
func probeLiveURL(ctx context.Context, hc *http.Client, source MediaSource, rawURL string) (liveProbe, error) {
	probe, ranged, err := doLiveProbe(ctx, hc, source, rawURL)
	if err == nil {
		return probe, nil
	}
	// A range request is an optimization, not a requirement: retry once
	// without it when a server refuses the range.
	if ranged {
		if unpaged, _, retryErr := doLiveProbe(ctx, hc, source, rawURL); retryErr == nil {
			return unpaged, nil
		}
	}
	return liveProbe{}, err
}

// doLiveProbe performs one ranged request, reporting whether the range header
// was actually accepted by the server.
func doLiveProbe(ctx context.Context, hc *http.Client, source MediaSource, rawURL string) (liveProbe, bool, error) {
	req, err := newLiveProbeRequest(ctx, source, rawURL)
	if err != nil {
		return liveProbe{}, false, err
	}
	req.Header.Set("Range", fmt.Sprintf("bytes=0-%d", liveProbeBodyBytes-1))

	resp, err := hc.Do(req)
	if err != nil {
		return liveProbe{}, true, fmt.Errorf("verify live stream: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return liveProbe{}, resp.StatusCode != http.StatusRequestedRangeNotSatisfiable,
			&HTTPError{Code: resp.StatusCode, URL: rawURL}
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, liveProbeBodyBytes))
	if err != nil {
		return liveProbe{}, true, fmt.Errorf("verify live stream: read response: %w", err)
	}
	return liveProbe{body: body, contentType: resp.Header.Get("Content-Type")}, resp.StatusCode == http.StatusPartialContent, nil
}

// newLiveProbeRequest builds a request carrying the same transport headers
// playback sends for this source.
func newLiveProbeRequest(ctx context.Context, source MediaSource, rawURL string) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, fmt.Errorf("verify live stream: create request: %w", err)
	}
	req.Header.Set("Accept", "*/*")
	userAgent := strings.TrimSpace(source.UserAgent)
	if userAgent == "" {
		userAgent = config.DesktopUserAgent
	}
	req.Header.Set("User-Agent", userAgent)
	if referer := strings.TrimSpace(source.Referer); referer != "" {
		req.Header.Set("Referer", referer)
		if !source.SuppressOrigin {
			if origin := originFromReferer(referer); origin != "" {
				req.Header.Set("Origin", origin)
			}
		}
	}
	if cookie := strings.TrimSpace(source.CookieHeader); cookie != "" {
		req.Header.Set("Cookie", cookie)
	}
	return req, nil
}

// verifyHLSPlaylist follows a master manifest into one working variant and
// then probes the first media segment.
func verifyHLSPlaylist(ctx context.Context, hc *http.Client, source MediaSource, playlistURL string, body []byte) error {
	mediaURL := playlistURL
	mediaBody := body
	if variants := hlsVariantURLs(playlistURL, body); len(variants) > 0 {
		mediaURL, mediaBody = "", nil
		var lastErr error
		for i, variantURL := range variants {
			if i >= maxHLSVariantProbes {
				break
			}
			probe, err := probeLiveURL(ctx, hc, source, variantURL)
			if err != nil {
				lastErr = err
				continue
			}
			if !looksLikeHLSPlaylist(probe.body) {
				lastErr = fmt.Errorf("verify live stream: variant %s is not an HLS playlist", variantURL)
				continue
			}
			mediaURL, mediaBody = variantURL, probe.body
			break
		}
		if mediaURL == "" {
			if lastErr == nil {
				lastErr = fmt.Errorf("verify live stream: no playable HLS variant")
			}
			return lastErr
		}
	}

	segmentURL := firstHLSSegmentURL(mediaURL, mediaBody)
	if segmentURL == "" {
		return nil
	}
	if err := probeLiveSegment(ctx, hc, source, segmentURL); err != nil {
		return fmt.Errorf("verify live stream: first segment: %w", err)
	}
	return nil
}

// probeLiveSegment checks that the first media segment can actually be
// fetched. A playlist pointing at blocked or expired segment paths passes a
// manifest-only probe but fails immediately in the player.
func probeLiveSegment(ctx context.Context, hc *http.Client, source MediaSource, segmentURL string) error {
	probe, _, err := doLiveProbe(ctx, hc, source, segmentURL)
	if err != nil {
		return err
	}
	if len(probe.body) == 0 {
		return fmt.Errorf("empty segment body")
	}
	if strings.Contains(strings.ToLower(probe.contentType), "text/html") {
		return fmt.Errorf("segment returned an HTML error page")
	}
	return nil
}

// hlsVariantURLs returns absolute child-playlist URLs declared by a master
// manifest, in declaration order.
func hlsVariantURLs(baseURL string, body []byte) []string {
	var variants []string
	lines := strings.Split(string(body), "\n")
	for i := 0; i < len(lines); i++ {
		line := strings.TrimSpace(lines[i])
		if !strings.HasPrefix(line, "#EXT-X-STREAM-INF") {
			continue
		}
		for j := i + 1; j < len(lines); j++ {
			candidate := strings.TrimSpace(lines[j])
			if candidate == "" || strings.HasPrefix(candidate, "#") {
				continue
			}
			if resolved := resolveHLSURL(baseURL, candidate); resolved != "" {
				variants = append(variants, resolved)
			}
			break
		}
	}
	return variants
}

// firstHLSSegmentURL returns the first media segment URL following an
// #EXTINF tag, resolved against the playlist URL.
func firstHLSSegmentURL(baseURL string, body []byte) string {
	lines := strings.Split(string(body), "\n")
	for i := 0; i < len(lines); i++ {
		if !strings.HasPrefix(strings.TrimSpace(lines[i]), "#EXTINF") {
			continue
		}
		for j := i + 1; j < len(lines); j++ {
			candidate := strings.TrimSpace(lines[j])
			if candidate == "" || strings.HasPrefix(candidate, "#") {
				continue
			}
			return resolveHLSURL(baseURL, candidate)
		}
	}
	return ""
}

// resolveHLSURL resolves a possibly relative playlist URI against its parent.
func resolveHLSURL(baseURL, rawURL string) string {
	base, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil {
		return strings.TrimSpace(rawURL)
	}
	ref, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return ""
	}
	return base.ResolveReference(ref).String()
}

// isHLSStream classifies a source by its declared type, URL shape, or the
// content type the server returned.
func isHLSStream(source MediaSource, contentType, rawURL string) bool {
	if source.Type == SourceTypeHLS || source.Type == SourceTypeM3U8 {
		return true
	}
	lowerURL := strings.ToLower(rawURL)
	if strings.Contains(lowerURL, ".m3u8") || strings.Contains(lowerURL, ".m3u") {
		return true
	}
	lowerType := strings.ToLower(contentType)
	return strings.Contains(lowerType, "mpegurl") || strings.Contains(lowerType, "application/vnd.apple")
}

// looksLikeHLSPlaylist reports whether body starts with the required HLS
// header after optional whitespace or a UTF-8 BOM.
func looksLikeHLSPlaylist(body []byte) bool {
	text := strings.TrimPrefix(string(body), "\ufeff")
	return strings.HasPrefix(strings.TrimSpace(text), "#EXTM3U")
}

// originFromReferer extracts scheme://host from a referer URL. It mirrors the
// player's Origin derivation so the probe sees the same headers playback
// sends.
func originFromReferer(referer string) string {
	for i := 0; i < len(referer); i++ {
		if referer[i] == ':' && i+3 <= len(referer) && referer[i+1] == '/' && referer[i+2] == '/' {
			end := strings.IndexAny(referer[i+3:], "/?#")
			if end == -1 {
				return referer
			}
			return referer[:i+3+end]
		}
	}
	return ""
}
