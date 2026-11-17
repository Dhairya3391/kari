// Package kit normalization: every provider maps raw upstream fields
// through these helpers so quality, languages, source kinds and dub/sub
// flags compare equal regardless of which provider produced them.
package kit

import (
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"kari/internal/lang"
	"kari/internal/provider"
)

// Canonical quality heights.
const (
	QualityUnknown = 0
	Quality360     = 360
	Quality480     = 480
	Quality576     = 576
	Quality720     = 720
	Quality1080    = 1080
	Quality1440    = 1440
	Quality2160    = 2160
)

// Quality tokens recognized in a raw label. Word boundaries keep backend
// names such as "HD1" or "4KHDHub" from being read as resolutions; an
// explicit WxH dimension is preferred so "1920x1080" folds by its height.
var (
	reQualityBracket   = regexp.MustCompile(`\[[^\]]+\]`)
	reQualityDimension = regexp.MustCompile(`(?i)\b\d{2,4}\s*x\s*(\d{3,4})\b`)
	reQuality4K        = regexp.MustCompile(`(?i)\b(4k|uhd|2160p?)\b`)
	reQualityQHD       = regexp.MustCompile(`(?i)\b(qhd|1440p?|2k)\b`)
	reQualityFHD       = regexp.MustCompile(`(?i)\b(fhd|1080p?)\b`)
	reQualityHD        = regexp.MustCompile(`(?i)\b(hd|720p?)\b`)
	reQuality576       = regexp.MustCompile(`(?i)\b(576p?)\b`)
	reQualitySD        = regexp.MustCompile(`(?i)\b(sd|480p?)\b`)
	reQuality360       = regexp.MustCompile(`(?i)\b(360p?)\b`)
	reQualityP         = regexp.MustCompile(`(?i)(\d{3,4})p`)
	reQualityNumber    = regexp.MustCompile(`\b(\d{3,4})\b`)
)

// NormalizeQuality is the canonical parser for a raw upstream quality label:
// it returns the canonical pixel height and the matching display label. Every
// provider, the filter pipeline, and HLS helpers route through here so the
// same text always sorts and renders identically.
func NormalizeQuality(raw string) (height int, label string) {
	height = qualityHeight(raw)
	return height, qualityLabel(height)
}

// QualityHeight returns just the canonical pixel height parsed by
// NormalizeQuality, for callers that only need ordering or a threshold.
func QualityHeight(raw string) int {
	height, _ := NormalizeQuality(raw)
	return height
}

// IsAdaptiveQuality reports whether a label only advertises an adaptive
// ("Auto"/"Master") stream without naming a real resolution. The manifest
// picks the tier at playback time, so ranking the row as a known resolution
// would be a guess.
func IsAdaptiveQuality(raw string) bool {
	stripped := raw
	if strings.IndexByte(raw, '[') >= 0 {
		stripped = reQualityBracket.ReplaceAllString(raw, "")
	}
	lower := strings.ToLower(strings.TrimSpace(stripped))
	if !strings.Contains(lower, "auto") && !strings.Contains(lower, "master") {
		return false
	}
	for _, re := range []*regexp.Regexp{
		reQuality4K, reQualityQHD, reQualityFHD, reQualityHD,
		reQuality576, reQualitySD, reQuality360, reQualityP,
	} {
		if re.MatchString(lower) {
			return false
		}
	}
	return true
}

// qualityHeight folds a raw quality label to a canonical pixel height. This
// is the single parsing path: explicit dimensions and resolutions win over
// adaptive words so "Auto 2160p" names a tier while bare "Auto" keeps the
// optimistic FHD guess used for ranking.
func qualityHeight(raw string) int {
	stripped := raw
	if strings.IndexByte(raw, '[') >= 0 {
		stripped = reQualityBracket.ReplaceAllString(raw, "")
	}
	lower := strings.ToLower(strings.TrimSpace(stripped))
	if lower == "" || lower == "unknown" || lower == "—" {
		return QualityUnknown
	}

	switch lower {
	case "1080p", "1080", "fhd":
		return Quality1080
	case "720p", "720", "hd":
		return Quality720
	case "2160p", "2160", "4k", "uhd":
		return Quality2160
	case "1440p", "1440", "qhd", "2k":
		return Quality1440
	case "576p", "576":
		return Quality576
	case "480p", "480", "sd":
		return Quality480
	case "360p", "360":
		return Quality360
	}

	if m := reQualityDimension.FindStringSubmatch(lower); len(m) >= 2 {
		if h, err := strconv.Atoi(m[1]); err == nil && h > 0 {
			return h
		}
	}
	for _, candidate := range []struct {
		re     *regexp.Regexp
		height int
	}{
		{reQuality4K, Quality2160},
		{reQualityQHD, Quality1440},
		{reQualityFHD, Quality1080},
		{reQualityHD, Quality720},
		{reQuality576, Quality576},
		{reQualitySD, Quality480},
		{reQuality360, Quality360},
	} {
		if candidate.re.MatchString(lower) {
			return candidate.height
		}
	}
	if m := reQualityP.FindStringSubmatch(lower); len(m) >= 2 {
		if h, err := strconv.Atoi(m[1]); err == nil && h > 0 {
			return h
		}
	}
	if strings.Contains(lower, "auto") || strings.Contains(lower, "master") || strings.Contains(lower, "live") {
		return Quality1080
	}
	if m := reQualityNumber.FindStringSubmatch(lower); len(m) >= 2 {
		if h, err := strconv.Atoi(m[1]); err == nil && h > 0 {
			return h
		}
	}
	return QualityUnknown
}

// qualityLabel maps a canonical height to its single display vocabulary.
// Keeping the mapping here means NormalizeQuality and the HLS helpers cannot
// drift apart on how a given height is spelled.
func qualityLabel(height int) string {
	switch {
	case height >= Quality2160:
		return "2160p"
	case height >= Quality1440:
		return "1440p"
	case height >= Quality1080:
		return "1080p"
	case height >= Quality720:
		return "720p"
	case height >= Quality576:
		return "576p"
	case height >= Quality480:
		return "480p"
	case height >= Quality360:
		return "360p"
	case height > 0:
		return fmt.Sprintf("%dp", height)
	default:
		return "unknown"
	}
}

// NormalizeAudio folds a raw audio language tag to ISO 639-1 via
// internal/lang. Empty stays empty (untagged tracks match any filter).
func NormalizeAudio(raw string) string {
	return lang.Normalize(raw)
}

// NormalizeSubtitle folds a raw subtitle language tag to ISO 639-1.
func NormalizeSubtitle(raw string) string {
	return lang.Normalize(raw)
}

// NormalizeSourceKind maps a stream URL or declared type to the shared
// source-kind vocabulary (hls/mp4). Unknown containers report mp4 so
// the player still attempts progressive playback.
func NormalizeSourceKind(urlOrType string) string {
	lower := strings.ToLower(strings.TrimSpace(urlOrType))
	if strings.Contains(lower, "m3u8") || strings.Contains(lower, "hls") || strings.Contains(lower, "m3u") {
		return provider.SourceTypeHLS
	}
	if strings.Contains(lower, "mpd") || strings.Contains(lower, "dash") {
		return "dash"
	}
	return provider.SourceTypeMP4
}

// NormalizeAudioMode folds a raw dub/sub flag to the AudioSub/AudioDub
// vocabulary. Empty stays empty (matches any selection).
func NormalizeAudioMode(raw string) string {
	lower := strings.ToLower(strings.TrimSpace(raw))
	switch {
	case strings.HasPrefix(lower, provider.AudioDub):
		return provider.AudioDub
	case strings.HasPrefix(lower, provider.AudioSub):
		return provider.AudioSub
	}
	return strings.TrimSpace(raw)
}

// DedupeSources drops blank sources and exact transport duplicates while
// preserving variants that require different headers or player options.
func DedupeSources(sources []provider.MediaSource) []provider.MediaSource {
	seen := make(map[string]struct{}, len(sources))
	out := make([]provider.MediaSource, 0, len(sources))
	for _, source := range sources {
		key := source.TransportIdentity()
		if key == "" {
			continue
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, source)
	}
	return out
}

// SortByQuality orders sources highest canonical height first, stable
// so provider priority order survives ties.
func SortByQuality(sources []provider.MediaSource) {
	slices.SortStableFunc(sources, func(a, b provider.MediaSource) int {
		hi, _ := NormalizeQuality(a.Quality)
		hj, _ := NormalizeQuality(b.Quality)
		return hj - hi
	})
}

// LocalStartsAt converts a parsed live start to local time. The zero
// value passes through (no schedule known). Callers must never render
// schedule text from Year.
func LocalStartsAt(t time.Time) time.Time {
	if t.IsZero() {
		return t
	}
	return t.Local()
}

var (
	reSeasonWord = regexp.MustCompile(`(?i)\b(?:season|s)\s*(\d+)\b`)
	reNthSeason  = regexp.MustCompile(`(?i)\b(\d+)(?:st|nd|rd|th)\s*season\b`)
	reRomanTwo   = regexp.MustCompile(`(?i)\bii\b`)
	reRomanThree = regexp.MustCompile(`(?i)\biii\b`)
	reRomanFour  = regexp.MustCompile(`(?i)\biv\b`)
)

// ParseSeason extracts a season number from a title string (e.g. "Season 2" -> 2, "2nd Season" -> 2, "II" -> 2).
// Returns 0 when no season indicator is found.
func ParseSeason(s string) int {
	if m := reSeasonWord.FindStringSubmatch(s); len(m) >= 2 {
		if n, err := strconv.Atoi(m[1]); err == nil {
			return n
		}
	}
	if m := reNthSeason.FindStringSubmatch(s); len(m) >= 2 {
		if n, err := strconv.Atoi(m[1]); err == nil {
			return n
		}
	}
	if reRomanTwo.MatchString(s) {
		return 2
	}
	if reRomanThree.MatchString(s) {
		return 3
	}
	if reRomanFour.MatchString(s) {
		return 4
	}
	return 0
}

// StripSeason removes season markers from a title string to leave the base series name.
func StripSeason(s string) string {
	s = reSeasonWord.ReplaceAllString(s, "")
	s = reNthSeason.ReplaceAllString(s, "")
	s = reRomanTwo.ReplaceAllString(s, "")
	s = reRomanThree.ReplaceAllString(s, "")
	s = reRomanFour.ReplaceAllString(s, "")
	return strings.TrimSpace(s)
}

// CanonicalTitle folds a title to bare alphanumerics with canonical season format
// for comparison, so punctuation, spacing, and season representations match cleanly.
func CanonicalTitle(s string) string {
	s = strings.ToLower(s)
	s = reNthSeason.ReplaceAllString(s, "season $1")
	s = reSeasonWord.ReplaceAllString(s, "season $1")
	s = reRomanTwo.ReplaceAllString(s, "season 2")
	s = reRomanThree.ReplaceAllString(s, "season 3")
	s = reRomanFour.ReplaceAllString(s, "season 4")

	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	return b.String()
}
