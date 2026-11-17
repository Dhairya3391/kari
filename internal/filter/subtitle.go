package filter

import (
	"kari/internal/provider"
	"kari/internal/provider/kit"
)

// SubtitleMode controls whether subtitle tracks influence source ordering.
type SubtitleMode uint8

// Subtitle modes.
const (
	// SubtitleOff ignores every subtitle signal, including hardsubs.
	SubtitleOff SubtitleMode = iota
	// SubtitlePreferred rewards tracks matching the configured language.
	SubtitlePreferred
	// SubtitleAny rewards any available subtitle track without a language test.
	SubtitleAny
)

// SubtitlePreference carries the user's subtitle-language choice plus the
// anime-specific burned-in subtitle preference.
type SubtitlePreference struct {
	// Mode selects whether subtitles affect ordering at all.
	Mode SubtitleMode
	// Language is the canonical preferred subtitle language.
	Language string
	// Fallback lists languages rewarded below the preferred one.
	Fallback []string
	// PreferHard gives anime hard-subs a soft boost when no track is attached.
	PreferHard bool
}

// SubtitleScore returns a soft ordering signal for src: +2 for a track in the
// preferred language, +1 for a fallback track, and 0 otherwise. It never
// removes a source from the candidate set. Anime hard-subs earn a +1 when the
// user wants them and the provider attached no selectable track.
func SubtitleScore(src provider.MediaSource, pref SubtitlePreference, mode provider.ContentType) int {
	if pref.Mode == SubtitleOff {
		return 0
	}

	codes := make([]string, 0, len(src.Subtitles))
	for _, sub := range src.Subtitles {
		if code := kit.SubtitleLanguage(sub.Language); code != "" {
			codes = append(codes, code)
		}
	}

	if pref.Mode == SubtitleAny {
		if len(codes) > 0 {
			return 1
		}
	} else {
		target := kit.SubtitleLanguage(pref.Language)
		if target != "" && target != "off" && containsCode(codes, target) {
			return 2
		}
		for _, raw := range pref.Fallback {
			fallback := kit.SubtitleLanguage(raw)
			if fallback != "" && fallback != "off" && containsCode(codes, fallback) {
				return 1
			}
		}
	}

	if pref.PreferHard && mode == provider.ModeAnime && len(src.Subtitles) == 0 &&
		src.SubType == provider.SubTypeHard {
		return 1
	}
	return 0
}

func containsCode(codes []string, target string) bool {
	for _, code := range codes {
		if code == target {
			return true
		}
	}
	return false
}
