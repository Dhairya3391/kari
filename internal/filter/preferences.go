package filter

import (
	"sort"
	"strings"

	"kari/internal/provider"
	"kari/internal/provider/kit"
)

// Preferences is the fully resolved ranking policy for one content mode.
type Preferences struct {
	// Quality selects the resolution hard filter.
	Quality QualityMode
	// Audio selects which audio languages may pass the hard filter.
	Audio LanguagePreference
	// Track selects the anime sub/dub track used as a soft ranking signal.
	Track provider.AudioTrack
	// Subtitles controls subtitle ordering only, never hard filtering.
	Subtitles SubtitlePreference
	// PreferredProvider names the resolver whose rows rank ahead of others.
	PreferredProvider string
}

// Scope holds default preferences and complete per-mode overrides. A mode
// present in ByMode is authoritative for every field; callers must seed it
// from Defaults before changing individual fields. This avoids a zero-valued
// override field (such as "subtitles off") being mistaken for "unset".
type Scope struct {
	// Defaults applies to every mode without an override.
	Defaults Preferences
	// ByMode holds complete mode-specific policies, keyed by content type.
	ByMode map[provider.ContentType]Preferences
}

// For returns the preferences for mode. A mode override wins outright; all
// other modes use Defaults.
func (s Scope) For(mode provider.ContentType) Preferences {
	if override, ok := s.ByMode[mode]; ok {
		return override
	}
	return s.Defaults
}

// FromLegacy converts the current settings fields into a Scope. Explicit
// download tiers (10-13) take precedence over the playback quality mode,
// matching the TUI's effective-download-quality behaviour. The anime override
// carries the sub/dub track and the burned-in subtitle preference.
func FromLegacy(qualityMode int, downloadQuality int, languageFilter map[string]bool, subtitleLanguage string, animeAudio string, disableAnimeSubs bool, preferredProvider string) Scope {
	effective := qualityMode
	if downloadQuality >= 10 && downloadQuality <= 13 {
		effective = downloadQuality
	}

	defaults := Preferences{
		Quality:           QualityModeFromLegacy(effective),
		Audio:             legacyLanguagePreference(languageFilter),
		Subtitles:         legacySubtitlePreference(subtitleLanguage, false),
		PreferredProvider: preferredProvider,
	}
	anime := defaults
	anime.Track = legacyTrack(animeAudio)
	if disableAnimeSubs {
		anime.Subtitles = SubtitlePreference{Mode: SubtitleOff}
	} else {
		anime.Subtitles = legacySubtitlePreference(subtitleLanguage, true)
	}
	return Scope{
		Defaults: defaults,
		ByMode:   map[provider.ContentType]Preferences{provider.ModeAnime: anime},
	}
}

// legacyLanguagePreference splits the dual-keyed settings map into canonical
// enabled and disabled codes. Sorting keeps the priority list deterministic
// because the source is a Go map with no stable iteration order.
func legacyLanguagePreference(filter map[string]bool) LanguagePreference {
	enabled, disabled := make([]string, 0, len(filter)), make([]string, 0, len(filter))
	for key, on := range filter {
		if on {
			enabled = append(enabled, key)
		} else {
			disabled = append(disabled, key)
		}
	}
	sort.Strings(enabled)
	sort.Strings(disabled)
	return NewLanguagePreference(enabled, disabled)
}

func legacySubtitlePreference(raw string, preferHard bool) SubtitlePreference {
	pref := SubtitlePreference{PreferHard: preferHard}
	switch trimmed := strings.TrimSpace(raw); {
	case strings.EqualFold(trimmed, "off"):
		pref.Mode = SubtitleOff
	case trimmed == "":
		pref.Mode = SubtitleAny
	default:
		pref.Mode = SubtitlePreferred
		pref.Language = kit.SubtitleLanguage(raw)
	}
	return pref
}

// legacyTrack folds the persisted anime-audio label into a track value. The
// settings store either a bare "sub"/"dub" or a display label such as
// "Dub · English", so both forms are accepted.
func legacyTrack(raw string) provider.AudioTrack {
	lower := strings.ToLower(strings.TrimSpace(raw))
	switch {
	case strings.Contains(lower, provider.AudioDub):
		return provider.TrackDub
	case strings.Contains(lower, provider.AudioSub):
		return provider.TrackSub
	case lower == string(provider.TrackDual):
		return provider.TrackDual
	default:
		return provider.TrackUnknown
	}
}
