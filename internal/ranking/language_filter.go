package ranking

import (
	"strings"

	"kari/internal/lang"
	"kari/internal/provider"
)

// FilterAudioLanguages removes sources whose declared audio language is
// disabled. Untagged sources use the configured English/original preference.
func FilterAudioLanguages(sources []provider.MediaSource, mode provider.ContentType, enabled map[string]bool) []provider.MediaSource {
	if mode == provider.ModeAnime || len(enabled) == 0 {
		return sources
	}

	filtered := make([]provider.MediaSource, 0, len(sources))
	for _, source := range sources {
		if audioLanguageEnabled(source, enabled) {
			filtered = append(filtered, source)
		}
	}
	return filtered
}

func audioLanguageEnabled(source provider.MediaSource, enabled map[string]bool) bool {
	raw := strings.TrimSpace(source.Language)
	if raw == "" {
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

func languageEnabled(enabled map[string]bool, candidate string) bool {
	if value, ok := enabled[candidate]; ok && !value {
		return false
	}
	if value, ok := enabled[strings.ToLower(candidate)]; ok && !value {
		return false
	}
	return true
}
