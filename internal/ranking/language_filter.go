package ranking

import (
	"strings"

	"kari/internal/lang"
	"kari/internal/provider"
)

// FilterAudioLanguages removes sources whose declared audio language is
// disabled. Untagged on-demand sources use the configured English/original
// preference; untagged live sources are kept because the player cannot select
// a track that the catalog never named.
func FilterAudioLanguages(sources []provider.MediaSource, mode provider.ContentType, enabled map[string]bool) []provider.MediaSource {
	if mode == provider.ModeAnime || len(enabled) == 0 {
		return sources
	}

	filtered := make([]provider.MediaSource, 0, len(sources))
	for _, source := range sources {
		if audioLanguageEnabled(source, mode, enabled) {
			filtered = append(filtered, source)
		}
	}
	return filtered
}

func audioLanguageEnabled(source provider.MediaSource, mode provider.ContentType, enabled map[string]bool) bool {
	raw := strings.TrimSpace(source.Language)
	if raw == "" {
		if mode == provider.ModeLive {
			return liveAudioLabelEnabled(source.AudioLabel, enabled)
		}
		return languageEnabled(enabled, "en") && languageEnabled(enabled, "English")
	}

	code := lang.Normalize(raw)
	for _, candidate := range []string{raw, code, lang.Name(code)} {
		if candidate != "" && !languageEnabled(enabled, candidate) {
			return false
		}
	}
	return true
}

// liveAudioLabelEnabled keeps a live source when at least one language in its
// display label is enabled. A channel that broadcasts in English and Hindi
// must survive disabling either one.
func liveAudioLabelEnabled(label string, enabled map[string]bool) bool {
	label = strings.TrimSpace(label)
	if label == "" {
		return true
	}
	for _, part := range strings.Split(label, "·") {
		candidate := strings.TrimSpace(part)
		if candidate == "" {
			continue
		}
		if languageEnabled(enabled, candidate) {
			return true
		}
	}
	return false
}

func languageEnabled(enabled map[string]bool, candidate string) bool {
	if value, ok := enabled[candidate]; ok && !value {
		return false
	}
	if value, ok := enabled[strings.ToLower(candidate)]; ok && !value {
		return false
	}
	return true
}
