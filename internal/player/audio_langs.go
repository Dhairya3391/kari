package player

import (
	"strings"

	"kari/internal/lang"
	"kari/internal/model"
	"kari/internal/provider"
)

// audioLanguagesFor returns the ordered language preferences for track
// selection. The user's enabled list wins; a single source-declared code is
// the fallback for local files and sources resolved without a preference.
// It is shared by desktop and Android players, so it must stay build-tag free.
func audioLanguagesFor(source provider.MediaSource, media model.ResolvedMedia) []string {
	codes := media.AudioLanguages
	if len(codes) == 0 {
		if code := strings.TrimSpace(source.AudioPreference()); code != "" {
			codes = []string{code}
		}
	}
	kept := make([]string, 0, len(codes))
	for _, code := range codes {
		if !audioLanguageDisabled(code, media.DisabledAudioLanguages) {
			kept = append(kept, code)
		}
	}
	if len(kept) == 0 && len(codes) > 0 && !audioLanguageDisabled("en", media.DisabledAudioLanguages) {
		return []string{"en"}
	}
	return kept
}

// audioLanguageDisabled reports whether code was explicitly switched off.
func audioLanguageDisabled(code string, disabled []string) bool {
	canonical := lang.Normalize(code)
	for _, off := range disabled {
		if lang.Normalize(off) == canonical {
			return true
		}
	}
	return false
}
