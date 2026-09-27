package kit

import (
	"context"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"kari/internal/config"
)

var reHLSResolution = regexp.MustCompile(`(?i)RESOLUTION=(\d+)x(\d+)`)

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
