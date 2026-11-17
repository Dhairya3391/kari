package kit

import (
	"bufio"
	"context"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"kari/internal/config"
	"kari/internal/provider"
)

var (
	reHLSResolution     = regexp.MustCompile(`(?i)RESOLUTION=(\d+)x(\d+)`)
	reHLSBandwidth      = regexp.MustCompile(`(?i)(?:^|[:,])\s*BANDWIDTH=(\d+)`)
	reHLSTargetDuration = regexp.MustCompile(`(?i)^#EXT-X-TARGETDURATION:\s*([0-9.]+)`)
	reHLSMediaSequence  = regexp.MustCompile(`(?i)^#EXT-X-MEDIA-SEQUENCE:\s*(\d+)`)
	reHLSAudioGroup     = regexp.MustCompile(`(?i)\bAUDIO="([^"]*)"`)
	reHLSCodecs         = regexp.MustCompile(`(?i)\bCODECS="([^"]*)"`)
)

const (
	// hlsFingerprintBytes bounds how much of a playlist is read. The opening
	// segments are enough to identify an asset, and keeping the probe small
	// matters on metered connections.
	hlsFingerprintBytes = 32 << 10
	// hlsFingerprintSegments is how many leading EXTINF durations form the
	// media-playlist fingerprint. A long prefix makes accidental matches
	// between different encodes effectively impossible; it costs no extra
	// bytes because it stays inside the bounded playlist read.
	hlsFingerprintSegments = 256
)

// HLSAssetInfo describes an HLS playlist from a bounded probe.
type HLSAssetInfo struct {
	// Fingerprint identifies the media; equal values mean mirror copies.
	Fingerprint string
	// Dimension is the largest rendition dimension in a master playlist
	// (max of width/height). It is zero for media playlists, which carry no
	// resolution metadata.
	Dimension int
}

// ProbeHLSAsset reads a bounded playlist prefix and reports both its identity
// and, for master playlists, the best rendition dimension. Two manifests that
// describe the same rendition produce the same Fingerprint even when they live
// on different mirror hosts or carry different signed URLs.
//
// Media playlists are fingerprinted by their leading segment durations plus
// the protocol's sequence/target-duration headers. Master playlists are
// fingerprinted by their rendition ladder. Only a bounded prefix is read, so
// callers can use this to collapse mirror copies without fetching media.
func ProbeHLSAsset(ctx context.Context, hc *http.Client, rawURL string, headers map[string]string) HLSAssetInfo {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return HLSAssetInfo{}
	}
	req.Header.Set("User-Agent", config.DesktopUserAgent)
	req.Header.Set("Accept", "*/*")
	req.Header.Set("Range", fmt.Sprintf("bytes=0-%d", hlsFingerprintBytes-1))
	for k, v := range headers {
		if strings.TrimSpace(v) != "" {
			req.Header.Set(k, v)
		}
	}

	resp, err := hc.Do(req)
	if err != nil {
		return HLSAssetInfo{}
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
		return HLSAssetInfo{}
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, hlsFingerprintBytes))
	if err != nil {
		return HLSAssetInfo{}
	}
	return hlsAssetInfoFromText(string(body))
}

// hlsAssetInfoFromText parses a bounded playlist prefix into identity data.
func hlsAssetInfoFromText(text string) HLSAssetInfo {
	if !strings.Contains(text, "#EXTM3U") {
		return HLSAssetInfo{}
	}

	var (
		renditions []string
		durations  []string
		target     string
		sequence   string
		dimension  int
	)

	scanner := bufio.NewScanner(strings.NewReader(text))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		switch {
		case strings.HasPrefix(line, "#EXT-X-STREAM-INF:"):
			resolution := ""
			if m := reHLSResolution.FindStringSubmatch(line); len(m) == 3 {
				resolution = m[1] + "x" + m[2]
				width, _ := strconv.Atoi(m[1])
				height, _ := strconv.Atoi(m[2])
				if d := max(width, height); d > dimension {
					dimension = d
				}
			}
			bandwidth := ""
			if m := reHLSBandwidth.FindStringSubmatch(line); len(m) == 2 {
				bandwidth = m[1]
			}
			renditions = append(renditions, resolution+"@"+bandwidth)

		case reHLSTargetDuration.MatchString(line):
			target = reHLSTargetDuration.FindStringSubmatch(line)[1]

		case reHLSMediaSequence.MatchString(line):
			sequence = reHLSMediaSequence.FindStringSubmatch(line)[1]

		case strings.HasPrefix(line, "#EXTINF:"):
			if len(durations) >= hlsFingerprintSegments {
				continue
			}
			raw := strings.TrimPrefix(line, "#EXTINF:")
			if i := strings.IndexByte(raw, ','); i >= 0 {
				raw = raw[:i]
			}
			duration, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
			if err != nil {
				continue
			}
			durations = append(durations, strconv.FormatFloat(duration, 'f', 3, 64))
		}
	}

	if len(renditions) > 0 {
		sort.Strings(renditions)
		return HLSAssetInfo{
			Fingerprint: "master:" + strings.Join(renditions, ","),
			Dimension:   dimension,
		}
	}
	if len(durations) == 0 {
		return HLSAssetInfo{}
	}
	return HLSAssetInfo{
		Fingerprint: "media:" + target + ":" + sequence + ":" + strings.Join(durations, ","),
	}
}

// HLSVariant is one rendition advertised by an HLS master playlist.
type HLSVariant struct {
	// URL is the absolute address of the rendition playlist.
	URL string
	// Height is the declared vertical resolution, zero when the master omits
	// it.
	Height int
	// Bandwidth is the declared peak bitrate in bits per second, zero when
	// absent.
	Bandwidth int
	// AudioGroup names the EXT-X-MEDIA audio rendition the variant depends on,
	// empty when the variant's own playlist carries the audio. A variant with a
	// group is video-only: playing its URL directly loses the sound.
	AudioGroup string
	// Codecs is the variant's CODECS attribute (e.g. "avc1.4d4020,mp4a.40.2"),
	// empty when the master omits it. Callers use it to sanity-check a declared
	// resolution against the codec's real capability.
	Codecs string
}

// ParseHLSVariants expands an HLS master playlist into its renditions in
// manifest order, resolving relative rendition URIs against the master URL. A
// media playlist yields no variants and no error, so callers can treat every
// playlist uniformly. Fetch failures and non-200 responses return an error,
// letting callers keep the master URL as the only playable row.
func ParseHLSVariants(ctx context.Context, hc *http.Client, masterURL string, headers map[string]string) ([]HLSVariant, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, masterURL, nil)
	if err != nil {
		return nil, fmt.Errorf("parse hls variants: %w", err)
	}
	req.Header.Set("User-Agent", config.DesktopUserAgent)
	req.Header.Set("Accept", "*/*")
	for k, v := range headers {
		if strings.TrimSpace(v) != "" {
			req.Header.Set(k, v)
		}
	}

	resp, err := hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch hls master: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, &provider.HTTPError{Code: resp.StatusCode, URL: masterURL}
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, hlsFingerprintBytes))
	if err != nil {
		return nil, fmt.Errorf("read hls master: %w", err)
	}
	return hlsVariantsFromText(masterURL, string(body)), nil
}

// hlsVariantsFromText parses a bounded master playlist prefix into renditions.
func hlsVariantsFromText(masterURL, text string) []HLSVariant {
	if !strings.Contains(text, "#EXTM3U") {
		return nil
	}
	base, err := url.Parse(masterURL)
	if err != nil {
		return nil
	}

	var (
		variants   []HLSVariant
		height     int
		bandwidth  int
		audioGroup string
		codecs     string
		pending    bool
	)

	scanner := bufio.NewScanner(strings.NewReader(text))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		switch {
		case strings.HasPrefix(line, "#EXT-X-STREAM-INF:"):
			pending = true
			height = 0
			bandwidth = 0
			audioGroup = ""
			codecs = ""
			if m := reHLSAudioGroup.FindStringSubmatch(line); len(m) == 2 {
				audioGroup = m[1]
			}
			if m := reHLSCodecs.FindStringSubmatch(line); len(m) == 2 {
				codecs = m[1]
			}
			if m := reHLSResolution.FindStringSubmatch(line); len(m) == 3 {
				if h, err := strconv.Atoi(m[2]); err == nil {
					height = h
				}
			}
			if m := reHLSBandwidth.FindStringSubmatch(line); len(m) == 2 {
				if bw, err := strconv.Atoi(m[1]); err == nil {
					bandwidth = bw
				}
			}
		case pending && line != "" && !strings.HasPrefix(line, "#"):
			pending = false
			ref, err := url.Parse(line)
			if err != nil {
				continue
			}
			variants = append(variants, HLSVariant{
				URL:        base.ResolveReference(ref).String(),
				Height:     height,
				Bandwidth:  bandwidth,
				AudioGroup: audioGroup,
				Codecs:     codecs,
			})
		}
	}
	return variants
}

// SourceMetaFromVariants returns the best credible rendition height and the
// highest declared bandwidth in an HLS master's variant ladder. Credibility
// follows CredibleHeight so a 4K claim backed by a low AVC level cannot
// outrank a real 4K rendition. Empty or unreadable ladders report 0/0.
func SourceMetaFromVariants(variants []HLSVariant) (height, bitrate int) {
	for _, variant := range variants {
		if h := CredibleHeight(variant.Height, variant.Codecs); h > height {
			height = h
		}
		if variant.Bandwidth > bitrate {
			bitrate = variant.Bandwidth
		}
	}
	return height, bitrate
}

// DetectHLSQuality reads an HLS master playlist's best rendition dimension
// and labels it through NormalizeQuality so every quality string in the
// codebase shares one parser. Failures and variant-less masters report
// "auto". Extra headers (Referer, Origin) ride along.
func DetectHLSQuality(ctx context.Context, hc *http.Client, masterURL string, headers map[string]string) string {
	dim := HLSBestDimension(ctx, hc, masterURL, headers)
	if dim <= 0 {
		return "auto"
	}
	_, label := NormalizeQuality(hlsQualityFromDimension(dim))
	return label
}

// hlsQualityFromDimension buckets a max(width/height) dimension into the raw
// resolution label that NormalizeQuality folds. Thresholds use the larger
// edge so letterboxed encodes still land in the right tier.
func hlsQualityFromDimension(dim int) string {
	switch {
	case dim >= 3800:
		return "2160p"
	case dim >= 1700:
		return "1080p"
	case dim >= 1100:
		return "720p"
	case dim >= 700:
		return "480p"
	default:
		return "360p"
	}
}

// HLSBestDimension returns the largest rendition dimension (max of
// width/height, so letterboxed encodes still count) of an HLS master
// playlist, or 0 when unreadable.
func HLSBestDimension(ctx context.Context, hc *http.Client, masterURL string, headers map[string]string) int {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, masterURL, nil)
	if err != nil {
		return 0
	}
	req.Header.Set("User-Agent", config.DesktopUserAgent)
	req.Header.Set("Accept", "*/*")
	for k, v := range headers {
		if strings.TrimSpace(v) != "" {
			req.Header.Set(k, v)
		}
	}

	resp, err := hc.Do(req)
	if err != nil {
		return 0
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 65536))
	if err != nil {
		return 0
	}
	text := string(body)
	if !strings.Contains(text, "#EXTM3U") {
		return 0
	}
	best := 0
	for _, m := range reHLSResolution.FindAllStringSubmatch(text, -1) {
		if len(m) < 3 {
			continue
		}
		w, _ := strconv.Atoi(m[1])
		h, _ := strconv.Atoi(m[2])
		if d := max(w, h); d > best {
			best = d
		}
	}
	return best
}

// ParseHLSVariantsText parses an already-downloaded master playlist body into
// renditions, resolving relative URIs against masterURL. It lets callers that
// fetched a playlist for another purpose reuse the body instead of issuing a
// second request just to read its quality ladder.
func ParseHLSVariantsText(masterURL, text string) []HLSVariant {
	return hlsVariantsFromText(masterURL, text)
}

// QualityLabelFromHeight buckets a pixel height into the canonical quality
// label shared by providers, ranking, and display. All height-to-label
// decisions go through the same mapping as NormalizeQuality; the 4K display
// spelling and the non-positive fallback remain display-only overrides kept
// for existing callers.
func QualityLabelFromHeight(height int) string {
	if height >= 2160 {
		return "4K (2160p)"
	}
	if height <= 0 {
		return fmt.Sprintf("%dp", height)
	}
	return qualityLabel(height)
}

// RelabelAdaptiveQuality replaces the leading Auto/Master token in quality
// with label, preserving any parenthesized language or backend suffix. Labels
// that already name a resolution are returned unchanged.
func RelabelAdaptiveQuality(quality, label string) string {
	q := strings.TrimSpace(quality)
	if q == "" {
		return label
	}
	lower := strings.ToLower(q)
	tokenLen := 0
	switch {
	case strings.HasPrefix(lower, "auto"):
		tokenLen = len("auto")
	case strings.HasPrefix(lower, "master"):
		tokenLen = len("master")
	default:
		return quality
	}
	if tokenLen == len(q) {
		return label
	}
	next := q[tokenLen]
	if next != ' ' && next != '(' && next != '[' && next != '-' && next != ':' {
		return quality
	}
	return label + q[tokenLen:]
}

// CredibleHeight rejects a declared 4K height when the video codec cannot
// physically encode it. Some playlists advertise 3840x2160 with an H.264 level
// capped at 3.2 or 4.0, whose maximum frame size is roughly 720p/1080p; the
// master then serves a much smaller picture behind a 2160p label. Clamping
// those claims keeps false 4K rows from outranking real HEVC 4K sources.
func CredibleHeight(height int, codecs string) int {
	if height < 2160 {
		return height
	}
	level := avcLevel(codecs)
	if level == 0 || level >= 51 {
		return height // unknown or AVC 5.1+: keep the manifest's claim
	}
	if level >= 40 {
		return 1080
	}
	return 720
}

// avcLevel extracts the H.264 level_idc from an avc1/avc3 codec string such as
// "avc1.4d4020,mp4a.40.2" (0x20 = 32 = level 3.2). It returns 0 when the codec
// string is absent or not AVC.
func avcLevel(codecs string) int {
	for _, part := range strings.Split(codecs, ",") {
		part = strings.ToLower(strings.TrimSpace(part))
		if !strings.HasPrefix(part, "avc1.") && !strings.HasPrefix(part, "avc3.") {
			continue
		}
		raw := part[strings.IndexByte(part, '.')+1:]
		if len(raw) < 6 {
			return 0
		}
		level, err := hex.DecodeString(raw[len(raw)-2:])
		if err != nil || len(level) != 1 {
			return 0
		}
		return int(level[0])
	}
	return 0
}
