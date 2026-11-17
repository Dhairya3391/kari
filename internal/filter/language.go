package filter

import (
	"encoding/json"
	"sort"
	"strings"

	"kari/internal/lang"
	"kari/internal/provider"
	"kari/internal/provider/kit"
)

// unlistedPriority is returned by LanguagePreference.Priority for a code that
// is neither enabled nor disabled. It is deliberately large so listed codes
// always sort ahead while remaining safe to use in integer arithmetic.
const unlistedPriority = 1 << 30

// LanguagePreference holds the canonical audio-language codes the user wants
// in priority order and the ones they explicitly turned off. Unknown codes
// are allowed by default: only an explicit false entry can remove a source.
type LanguagePreference struct {
	// Enabled lists canonical codes, lower index meaning stronger preference.
	// It drives ordering; a code absent from it is still allowed.
	Enabled []string
	// Disabled holds canonical codes explicitly switched off by the user.
	Disabled map[string]struct{}
}

// NewLanguagePreference canonicalizes raw language tags into a preference.
// Disabled keys win over enabled ones and duplicates are removed so the
// resulting priority list is stable regardless of input order.
func NewLanguagePreference(enabled []string, disabled []string) LanguagePreference {
	disabledCodes := make(map[string]struct{}, len(disabled))
	for _, raw := range disabled {
		if code := lang.Normalize(raw); code != "" {
			disabledCodes[code] = struct{}{}
		}
	}

	seen := make(map[string]struct{}, len(enabled))
	enabledCodes := make([]string, 0, len(enabled))
	for _, raw := range enabled {
		code := lang.Normalize(raw)
		if code == "" {
			continue
		}
		if _, off := disabledCodes[code]; off {
			continue
		}
		if _, dup := seen[code]; dup {
			continue
		}
		seen[code] = struct{}{}
		enabledCodes = append(enabledCodes, code)
	}
	return LanguagePreference{Enabled: enabledCodes, Disabled: disabledCodes}
}

// MarshalJSON stores the preference as plain string lists so settings.json
// stays readable and a disabled set never leaks its internal map encoding.
func (p LanguagePreference) MarshalJSON() ([]byte, error) {
	disabled := make([]string, 0, len(p.Disabled))
	for code := range p.Disabled {
		disabled = append(disabled, code)
	}
	sort.Strings(disabled)
	return json.Marshal(struct {
		Enabled  []string `json:"enabled,omitempty"`
		Disabled []string `json:"disabled,omitempty"`
	}{Enabled: p.Enabled, Disabled: disabled})
}

// UnmarshalJSON restores a preference from the string-list form written by
// MarshalJSON, canonicalizing both lists through the same constructor.
func (p *LanguagePreference) UnmarshalJSON(data []byte) error {
	var raw struct {
		Enabled  []string `json:"enabled"`
		Disabled []string `json:"disabled"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	*p = NewLanguagePreference(raw.Enabled, raw.Disabled)
	return nil
}

// Allows reports whether code may reach the player. An empty or unknown tag
// is always allowed; a canonical code is allowed unless explicitly disabled.
func (p LanguagePreference) Allows(code string) bool {
	canonical := lang.Normalize(code)
	if canonical == "" {
		return true
	}
	if _, off := p.Disabled[canonical]; off {
		return false
	}
	raw := strings.ToLower(strings.TrimSpace(code))
	_, off := p.Disabled[raw]
	return !off
}

// Priority returns the zero-based preference index of code, or a large value
// when the code is unlisted. Lower values mean more preferred.
func (p LanguagePreference) Priority(code string) int {
	canonical := lang.Normalize(code)
	for index, enabled := range p.Enabled {
		if lang.Normalize(enabled) == canonical {
			return index
		}
	}
	return unlistedPriority
}

// FilterLanguages removes audio sources the user explicitly disabled. A
// source survives when any of its declared languages is allowed; untagged
// on-demand sources follow the English setting, while untagged anime and live
// sources stay because sub/dub or the channel itself carries the choice.
func FilterLanguages(sources []provider.MediaSource, mode provider.ContentType, pref LanguagePreference) []provider.MediaSource {
	return applyKeep(sources, languageFilterKeep(sources, mode, pref))
}

// languageFilterKeep computes the per-source language decisions once so the
// pipeline can reuse them while retaining original source indices.
func languageFilterKeep(sources []provider.MediaSource, mode provider.ContentType, pref LanguagePreference) []bool {
	keep := make([]bool, len(sources))
	for i, src := range sources {
		keep[i] = sourceAllowed(src, mode, pref)
	}
	return keep
}

func sourceAllowed(src provider.MediaSource, mode provider.ContentType, pref LanguagePreference) bool {
	declared := declaredLanguages(src, mode)
	if len(declared) == 0 {
		switch mode {
		case provider.ModeAnime, provider.ModeLive:
			return true
		default:
			return pref.Allows("en")
		}
	}
	for _, code := range declared {
		if pref.Allows(code) {
			return true
		}
	}
	return false
}

// declaredLanguages returns the canonical audio languages of a source. Typed
// Audio.Languages win, then the legacy Language field; live channels with no
// typed tag fall back to splitting their display label so disabling one of
// several broadcast languages does not drop the whole channel.
func declaredLanguages(src provider.MediaSource, mode provider.ContentType) []string {
	if len(src.Audio.Languages) > 0 {
		return lang.Canonicalize(src.Audio.Languages)
	}
	if raw := strings.TrimSpace(src.Language); raw != "" {
		return []string{lang.FromLabel(raw)}
	}
	if mode == provider.ModeLive {
		return lang.Canonicalize(kit.SplitAudioLabel(src.AudioLabel))
	}
	return nil
}
