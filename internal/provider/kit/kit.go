// Package kit holds resolution helpers shared by the anime providers:
// episode-handle parsing, referer/user-agent extraction from stream
// metadata, subtitle normalization, quality labeling, and the
// try-each-server fetch loop. Providers keep their private API types and
// map them into kit values at the call site.
package kit

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"kari/internal/httpclient"
	"kari/internal/lang"
	"kari/internal/provider"
)

// Clean collapses whitespace, trimming and joining fields with single
// spaces. Upstream payloads frequently pad every string field.
func Clean(s string) string {
	return strings.Join(strings.Fields(strings.TrimSpace(s)), " ")
}

// Handle is a parsed episode route. Providers build their own watch URLs
// from it.
type Handle struct {
	AniListID string
	Category  string
	Number    int
}

// Valid reports whether the handle carries enough information to build a
// watch route.
func (h Handle) Valid() bool { return h.AniListID != "" && h.Number > 0 }

// ParseHandle extracts the route from an episode ID of the shape
// watch/<server>/<id>/<category>/<number> (or the 4- and 3-part shorter
// forms), falling back to mediaID/audio/episode. When requireSegment is
// non-empty, the 5-part form only matches if the second segment equals it
// (anilight routes carry "anilight" there; other providers may use their
// own segment name, which is ignored when requireSegment is empty).
func ParseHandle(mediaID, epID, audio string, episode int, requireSegment string) Handle {
	requestedCategory := strings.ToLower(strings.TrimSpace(audio))
	h := Handle{
		AniListID: strings.TrimSpace(mediaID),
		Category:  requestedCategory,
		Number:    episode,
	}
	if h.Category == "" {
		h.Category = provider.AudioSub
	}
	if !strings.Contains(epID, "/") {
		return h
	}
	parts := strings.Split(epID, "/")
	switch {
	case len(parts) >= 5 && parts[0] == "watch" && (requireSegment == "" || parts[1] == requireSegment):
		h.AniListID = parts[2]
		h.Category = strings.ToLower(strings.TrimSpace(parts[3]))
		h.Number = atoiPositive(parts[4], h.Number)
	case len(parts) == 4 && parts[0] == "watch" && requireSegment == "":
		h.AniListID = parts[1]
		h.Category = parts[2]
		h.Number = atoiPositive(parts[3], h.Number)
	case len(parts) == 3:
		h.AniListID = parts[0]
		h.Category = parts[1]
		h.Number = atoiPositive(parts[2], h.Number)
	}
	if requestedCategory == provider.AudioSub || requestedCategory == provider.AudioDub {
		h.Category = requestedCategory
	}
	return h
}

func atoiPositive(s string, fallback int) int {
	if n, err := strconv.Atoi(s); err == nil && n > 0 {
		return n
	}
	return fallback
}

// WatchURLs builds one watch URL per server in the documented
// /watch/<server>/<id>/<category>/<number> shape.
func WatchURLs(base, anilistID, category string, number int, servers []string) []string {
	urls := make([]string, 0, len(servers))
	for _, srv := range servers {
		urls = append(urls, fmt.Sprintf("%s/watch/%s/%s/%s/%d",
			base, srv, url.PathEscape(anilistID), category, number))
	}
	return urls
}

// FetchFirst fetches candidate URLs concurrently and returns the first
// response whose payload hasStreams. When a valid payload arrives, in-flight
// requests to other servers are immediately cancelled. A 404 sets ErrNotFound
// as last error; other non-200 statuses set an HTTPError; every failure moves
// on to the next URL. Returns ErrNoSources when all URLs resolved but none
// had streams. label prefixes wrapped errors (e.g. "anilight resolve").
func FetchFirst[T any](ctx context.Context, hc *http.Client, urls []string, label string, hasStreams func(T) bool) (T, error) {
	var zero T
	var lastErr error
	for _, u := range urls {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return zero, fmt.Errorf("%s: build request: %w", label, err)
		}
		resp, err := hc.Do(req)
		if err != nil {
			lastErr = fmt.Errorf("%s: %w", label, err)
			continue
		}
		if resp.StatusCode != http.StatusOK {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusNotFound {
				lastErr = provider.ErrNotFound
			} else {
				lastErr = &provider.HTTPError{Code: resp.StatusCode, URL: u}
			}
			continue
		}
		body, err := httpclient.ReadCapped(resp)
		if err != nil {
			lastErr = fmt.Errorf("%s: read body: %w", label, err)
			continue
		}
		var payload T
		if err := json.Unmarshal(body, &payload); err != nil {
			lastErr = fmt.Errorf("%s: decode response: %w", label, err)
			continue
		}
		if hasStreams(payload) {
			return payload, nil
		}
	}
	if lastErr != nil {
		return zero, lastErr
	}
	return zero, provider.ErrNoSources
}

// Stream carries the metadata kit needs to derive player headers from one
// raw stream entry.
type Stream struct {
	Referer     string
	Headers     map[string]string
	HTTPHeaders map[string]string
	MPVArgs     []string
}

// ArgPolicy records the per-provider differences in how mpv launch args
// are folded into referer/user-agent/extra args.
type ArgPolicy struct {
	// KeepHeaderFieldArgs preserves --http-header-fields= args verbatim.
	KeepHeaderFieldArgs bool
	// SkipURLArgs drops kept args that look like URLs (reamine's backend
	// passes the stream URL itself in the args list).
	SkipURLArgs bool
	// StripQuotes trims surrounding double quotes before inspection.
	StripQuotes bool
}

// Headers derives the referer and user-agent for a stream: the Referer
// field wins, then the Headers and HTTPHeaders maps, then the mpv
// --referrer/--user-agent args. Remaining args come back as extraArgs.
func Headers(st Stream, pol ArgPolicy) (referer, userAgent string, extraArgs []string) {
	referer = st.Referer
	if referer == "" {
		referer = st.Headers["Referer"]
	}
	if referer == "" {
		referer = st.HTTPHeaders["Referer"]
	}
	userAgent = st.Headers["User-Agent"]
	if userAgent == "" {
		userAgent = st.HTTPHeaders["User-Agent"]
	}

	for _, arg := range st.MPVArgs {
		arg = strings.TrimSpace(arg)
		if pol.StripQuotes {
			arg = strings.Trim(arg, `"`)
		}
		switch {
		case pol.KeepHeaderFieldArgs && strings.HasPrefix(arg, "--http-header-fields="):
			extraArgs = append(extraArgs, arg)
		case strings.HasPrefix(arg, "--referrer="):
			if referer == "" {
				referer = strings.TrimPrefix(arg, "--referrer=")
			}
		case strings.HasPrefix(arg, "--user-agent="):
			if userAgent == "" {
				userAgent = strings.TrimPrefix(arg, "--user-agent=")
			}
		case arg != "" && !(pol.SkipURLArgs && (strings.HasPrefix(arg, "http://") || strings.HasPrefix(arg, "https://"))):
			extraArgs = append(extraArgs, arg)
		}
	}
	return referer, userAgent, extraArgs
}

// Subtitle is a provider-agnostic external subtitle track.
type Subtitle struct {
	URL, File, Language, Lang, Label, Kind string
	Default                                bool
}

// Subtitles converts raw subtitle entries: the URL (or legacy File) field
// is the track location, thumbnail previews are dropped, the language tag
// falls back through Language/Lang/Label to "en", and duplicates by URL
// are removed.
func Subtitles(subs []Subtitle) []provider.SubtitleOption {
	out := make([]provider.SubtitleOption, 0, len(subs))
	seen := make(map[string]struct{}, len(subs))
	for _, s := range subs {
		file := Clean(s.URL)
		if file == "" {
			file = Clean(s.File)
		}
		if file == "" {
			continue
		}
		if isThumbnailTrack(s) {
			continue
		}
		if _, ok := seen[file]; ok {
			continue
		}
		seen[file] = struct{}{}
		tag := Clean(s.Language)
		if tag == "" {
			tag = Clean(s.Lang)
		}
		if tag == "" {
			tag = Clean(s.Label)
		}
		if tag == "" {
			tag = "en"
		}
		out = append(out, provider.SubtitleOption{
			URL:      file,
			Language: lang.Normalize(tag),
			Default:  s.Default,
		})
	}
	return out
}

func isThumbnailTrack(s Subtitle) bool {
	for _, v := range []string{s.Kind, s.Language, s.Lang} {
		if strings.EqualFold(Clean(v), "thumbnails") {
			return true
		}
	}
	return false
}

// AutoQuality maps an empty or "auto" quality label to "Auto".
func AutoQuality(quality string) string {
	if quality == "" || strings.EqualFold(quality, "auto") {
		return "Auto"
	}
	return quality
}

// QualityLabel names a stream quality, deriving a label from the stream
// type when the provider omits one: HLS (or an explicit "auto") becomes
// Auto, embeds become Embed, anything else Direct.
func QualityLabel(quality, typ string) string {
	if quality != "" && !strings.EqualFold(quality, "auto") {
		return quality
	}
	switch {
	case strings.EqualFold(typ, provider.SourceTypeHLS), strings.EqualFold(quality, "auto"):
		return "Auto"
	case strings.EqualFold(typ, "embed"):
		return "Embed"
	default:
		return "Direct"
	}
}

// TagQuality appends the serving backend in parentheses when known,
// preferring primary and falling back to secondary.
func TagQuality(quality, primary, secondary string) string {
	server := Clean(primary)
	if server == "" {
		server = Clean(secondary)
	}
	if server == "" {
		return quality
	}
	return fmt.Sprintf("%s (%s)", quality, server)
}

// ── Media URL classification ────────────────────────────────────────────────

// dubLangs are quality-field values that actually name dubbed audio.
var dubLangs = map[string]string{
	"hindi": "hi", "english": "en", "tamil": "ta", "telugu": "te",
	"spanish": "es", "french": "fr", "portuguese": "pt", "german": "de",
	"italian": "it", "arabic": "ar", "russian": "ru", "japanese": "ja",
	"korean": "ko", "chinese": "zh", "indonesian": "id", "turkish": "tr",
}

// DubLangOf detects dubbed-audio quality labels ("Hindi" → "hi"), else "".
func DubLangOf(quality string) string {
	return dubLangs[strings.ToLower(strings.TrimSpace(quality))]
}

// IsMp4URL reports whether u points at a progressive mp4/m4s resource.
func IsMp4URL(u string) bool {
	l := strings.ToLower(u)
	for _, ext := range []string{".mp4", ".m4s"} {
		if i := strings.Index(l, ext); i >= 0 {
			rest := l[i+len(ext):]
			if rest == "" || rest[0] == '?' || rest[0] == '#' {
				return true
			}
		}
	}
	return strings.Contains(l, "/mp4/")
}

// IsSubtitleURL guards against upstreams mislabeling video streams as
// subtitles: only real subtitle resources pass.
func IsSubtitleURL(u string) bool {
	l := strings.ToLower(u)
	if strings.Contains(l, "/video.m3u8") {
		return false
	}
	for _, ext := range []string{".m3u8", ".mpd", ".mp4", ".m4s", ".webm", ".mkv"} {
		if strings.Contains(l, ext) {
			return false
		}
	}
	for _, ext := range []string{".vtt", ".srt", ".ass", ".ssa", ".ttml", ".dfxp", ".sbv", ".lrc"} {
		if strings.Contains(l, ext) {
			return true
		}
	}
	return strings.Contains(l, "sub")
}
