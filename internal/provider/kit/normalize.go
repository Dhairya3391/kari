// Package kit normalization: every provider maps raw upstream fields
// through these helpers so quality, languages, source kinds and dub/sub
// flags compare equal regardless of which provider produced them.
package kit

import (
	"slices"
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
	Quality720     = 720
	Quality1080    = 1080
	Quality1440    = 1440
	Quality2160    = 2160
)

// NormalizeQuality maps a raw upstream quality string to a canonical
// height and label. Heights: 2160/1080/720/480/unknown. Labels use the
// 2160p/1080p/720p/480p/unknown vocabulary so ranking and display agree.
func NormalizeQuality(raw string) (height int, label string) {
	lower := strings.ToLower(strings.TrimSpace(raw))
	if lower == "" || lower == "unknown" {
		return QualityUnknown, "unknown"
	}
	// Explicit adaptive labels win over backend tags: "Auto (HD1)" is an
	// adaptive master, not 720p. Numeric heights beat word labels so a
	// backend tag like "[4KHDHub]" never overrides an explicit "1080p".
	if strings.Contains(lower, "auto") || strings.Contains(lower, "master") || strings.Contains(lower, "live") {
		return Quality1080, "1080p"
	}
	switch {
	case strings.Contains(lower, "2160"):
		return Quality2160, "2160p"
	case strings.Contains(lower, "1080"):
		return Quality1080, "1080p"
	case strings.Contains(lower, "1440"):
		return Quality1440, "1440p"
	case strings.Contains(lower, "720"):
		return Quality720, "720p"
	case strings.Contains(lower, "480"):
		return Quality480, "480p"
	case strings.Contains(lower, "360"):
		return Quality360, "360p"
	case strings.Contains(lower, "4k") || strings.Contains(lower, "uhd"):
		return Quality2160, "2160p"
	case strings.Contains(lower, "qhd") || strings.Contains(lower, "2k"):
		return Quality1440, "1440p"
	case strings.Contains(lower, "fhd"):
		return Quality1080, "1080p"
	case strings.Contains(lower, "hd"):
		return Quality720, "720p"
	case strings.Contains(lower, "sd"):
		return Quality480, "480p"
	}
	return QualityUnknown, "unknown"
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

// BackendTag extracts the [Backend] token from a quality label such as
// "4K [4KHDHub] (Hindi)" or "1080p (Vidstream-2)". Parenthesized server
// names count too; bare resolutions and language suffixes ("(Hindi)")
// do not — they are not backends.
func BackendTag(quality string) string {
	open := strings.Index(quality, "[")
	if open >= 0 {
		if close := strings.Index(quality[open:], "]"); close > 0 {
			if tag := strings.TrimSpace(quality[open+1 : open+close]); tag != "" {
				return tag
			}
		}
	}
	open = strings.Index(quality, "(")
	if open >= 0 {
		if close := strings.Index(quality[open:], ")"); close > 0 {
			if tag := strings.TrimSpace(quality[open+1 : open+close]); tag != "" && !isLanguageTag(tag) {
				return tag
			}
		}
	}
	return ""
}

// isLanguageTag reports whether a parenthesized token is a language
// suffix (e.g. "(Hindi)") rather than a backend name.
func isLanguageTag(tag string) bool {
	switch strings.ToLower(strings.TrimSpace(tag)) {
	case "hindi", "english", "tamil", "telugu", "spanish", "french", "german",
		"japanese", "korean", "chinese", "arabic", "dub", "sub", "multi":
		return true
	}
	return false
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
