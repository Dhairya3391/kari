package kit

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"kari/internal/config"
)

var (
	reHLSResolution     = regexp.MustCompile(`(?i)RESOLUTION=(\d+)x(\d+)`)
	reHLSBandwidth      = regexp.MustCompile(`(?i)BANDWIDTH=(\d+)`)
	reHLSTargetDuration = regexp.MustCompile(`(?i)^#EXT-X-TARGETDURATION:\s*([0-9.]+)`)
	reHLSMediaSequence  = regexp.MustCompile(`(?i)^#EXT-X-MEDIA-SEQUENCE:\s*(\d+)`)
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
	// Range keeps cooperating CDNs from sending the whole playlist; servers
	// that ignore it are still bounded by the LimitReader below.
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

// DetectHLSQuality reads an HLS master playlist's best rendition dimension
// and labels it (2160p/1080p/720p/480p/360p); failures and variant-less
// masters report "auto". Extra headers (Referer, Origin) ride along.
func DetectHLSQuality(ctx context.Context, hc *http.Client, masterURL string, headers map[string]string) string {
	dim := HLSBestDimension(ctx, hc, masterURL, headers)
	switch {
	case dim >= 3800:
		return "2160p"
	case dim >= 1700:
		return "1080p"
	case dim >= 1100:
		return "720p"
	case dim >= 700:
		return "480p"
	case dim > 0:
		return "360p"
	default:
		return "auto"
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
