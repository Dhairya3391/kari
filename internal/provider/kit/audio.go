package kit

import (
	"strings"

	"kari/internal/lang"
)

// AudioLabel joins language tags into one display-only audio description.
// It is for UI text only: filtering reads the typed Language field, never
// this string. The " · " delimiter comes from lang.Names, the same helper
// providers use, so multi-language channels render identically everywhere.
func AudioLabel(langs []string) string {
	return lang.Names(langs)
}

// SplitAudioLabel reverses AudioLabel into individual display names. It
// accepts both the canonical " · " separator and a bare "·" so older
// persisted labels and upstream strings split the same way. The result is
// display-only; use NormalizeAudio when a filterable language code is needed.
func SplitAudioLabel(label string) []string {
	parts := strings.Split(label, "·")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

// SubtitleLanguage resolves a raw subtitle label to a canonical language code
// via lang.FromLabel. Subtitle filtering shares this one parsing path instead
// of each caller scrubbing provider-supplied labels on its own.
func SubtitleLanguage(raw string) string {
	return lang.FromLabel(raw)
}
