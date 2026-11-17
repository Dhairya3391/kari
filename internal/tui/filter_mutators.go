package tui

import (
	"strings"

	"kari/internal/filter"
	"kari/internal/lang"
	"kari/internal/provider"
)

// modePreferences returns the complete, authoritative policy for mode.
// Scope.For returns a value, so callers may edit the result freely before
// handing it back to setModePreferences.
func (m *modelImpl) modePreferences(mode provider.ContentType) filter.Preferences {
	return m.filterScope.For(mode)
}

// setModePreferences writes a complete policy for mode, creating the override
// map on first use. Every mutator copies a known-good policy before calling
// this so an all-zero field (for example SubtitleOff) stays intentional.
func (m *modelImpl) setModePreferences(mode provider.ContentType, prefs filter.Preferences) {
	if m.filterScope.ByMode == nil {
		m.filterScope.ByMode = make(map[provider.ContentType]filter.Preferences)
	}
	m.filterScope.ByMode[mode] = prefs
}

// qualityCycle is the stream-quality order shown to the user. Returning a
// fresh slice avoids a package-level mutable.
func qualityCycle() []filter.QualityMode {
	return []filter.QualityMode{
		filter.QualityAll,
		filter.QualityHighest,
		filter.QualityDataSaver,
		filter.QualityLowest,
		filter.QualityUHD,
		filter.QualityFHD,
		filter.QualityHD,
		filter.QualitySD,
	}
}

// audioTrackCycle is the anime track order. TrackUnknown renders as "Auto"
// and participates so the legacy default remains reachable.
func audioTrackCycle() []provider.AudioTrack {
	return []provider.AudioTrack{
		provider.TrackUnknown,
		provider.TrackSub,
		provider.TrackDub,
		provider.TrackDual,
	}
}

// setQuality writes the quality policy for mode and mirrors the legacy
// persisted field from the active mode's policy for downgrade compatibility.
func (m *modelImpl) setQuality(mode provider.ContentType, q filter.QualityMode) {
	prefs := m.modePreferences(mode)
	prefs.Quality = q
	m.setModePreferences(mode, prefs)
	m.qualityMode = legacyQuality(m.modePreferences(m.appMode).Quality)
}

// cycleQuality steps the active mode's quality by delta.
func (m *modelImpl) cycleQuality(mode provider.ContentType, delta int) {
	cycle := qualityCycle()
	current := m.modePreferences(mode).Quality
	index := 0
	for i, q := range cycle {
		if q == current {
			index = i
			break
		}
	}
	m.setQuality(mode, cycle[(index+delta+len(cycle))%len(cycle)])
}

// setAudioLanguages writes the global audio-language preference into Defaults
// and every existing per-mode override. Legacy language filters were global,
// so the picker must not leave an anime override behind with a stale list.
func (m *modelImpl) setAudioLanguages(pref filter.LanguagePreference) {
	m.filterScope.Defaults.Audio = pref
	for mode, modePrefs := range m.filterScope.ByMode {
		modePrefs.Audio = pref
		m.filterScope.ByMode[mode] = modePrefs
	}
	m.syncLegacyLanguageFilter()
}

// toggleAudioLanguage flips one canonical language code in the global audio
// preference, preserving the priority order of the remaining enabled codes.
// The last enabled language cannot be switched off.
func (m *modelImpl) toggleAudioLanguage(l provider.AudioLanguage) {
	pref := m.filterScope.Defaults.Audio
	code := lang.Normalize(l.Code)
	if code == "" {
		code = lang.Normalize(l.Display)
	}
	if code == "" {
		return
	}

	enabled := append([]string(nil), pref.Enabled...)
	disabled := make([]string, 0, len(pref.Disabled)+1)
	for existing := range pref.Disabled {
		disabled = append(disabled, existing)
	}

	if pref.Allows(l.Code) && pref.Allows(l.Display) {
		disabled = append(disabled, code)
		enabled = removeCode(enabled, code)
	} else {
		enabled = append(enabled, code)
		disabled = removeCode(disabled, code)
	}

	next := filter.NewLanguagePreference(enabled, disabled)
	if !m.scopeHasEnabledLanguage(next) {
		// Keep the previous policy when the toggle would empty the pool.
		return
	}
	m.setAudioLanguages(next)
}

// scopeHasEnabledLanguage reports whether at least one known audio language
// remains enabled. An empty language pool means the registry could not supply
// candidates, so the toggle is left unrestricted.
func (m *modelImpl) scopeHasEnabledLanguage(pref filter.LanguagePreference) bool {
	var languages []provider.AudioLanguage
	if m.registry != nil {
		languages = m.registry.AudioLanguages(provider.ModeMovies, provider.ModeTV)
	}
	if len(languages) == 0 {
		return true
	}
	for _, l := range languages {
		if pref.Allows(l.Code) || pref.Allows(l.Display) {
			return true
		}
	}
	return false
}

// removeCode drops code from a canonical code list without preserving order
// of the remaining elements (callers re-sort through NewLanguagePreference).
func removeCode(codes []string, code string) []string {
	out := codes[:0]
	for _, existing := range codes {
		if existing != code {
			out = append(out, existing)
		}
	}
	return out
}

// setAudioTrack writes the anime track policy and mirrors the legacy audio
// mode used for episode selection and history.
func (m *modelImpl) setAudioTrack(track provider.AudioTrack) {
	prefs := m.modePreferences(provider.ModeAnime)
	prefs.Track = track
	m.setModePreferences(provider.ModeAnime, prefs)
	m.audioMode = trackToAudioMode(track)
}

// toggleAnimeAudioTrack preserves the legacy 'a' sub/dub flip while writing
// the same typed track policy used by the settings screen.
func (m *modelImpl) toggleAnimeAudioTrack() {
	if m.modePreferences(provider.ModeAnime).Track == provider.TrackDub {
		m.setAudioTrack(provider.TrackSub)
		return
	}
	m.setAudioTrack(provider.TrackDub)
}

// cycleAudioTrack steps the anime track through Auto/Sub/Dub/Dual.
func (m *modelImpl) cycleAudioTrack(delta int) {
	cycle := audioTrackCycle()
	current := m.modePreferences(provider.ModeAnime).Track
	index := 0
	for i, track := range cycle {
		if track == current {
			index = i
			break
		}
	}
	m.setAudioTrack(cycle[(index+delta+len(cycle))%len(cycle)])
}

// trackToAudioMode folds a typed track into the legacy sub/dub string used by
// episode fetching. Dual and unknown both fall back to sub.
func trackToAudioMode(track provider.AudioTrack) string {
	if track == provider.TrackDub {
		return provider.AudioDub
	}
	return provider.AudioSub
}

// trackLabel renders a track without embedding a language name.
func trackLabel(track provider.AudioTrack) string {
	switch track {
	case provider.TrackSub:
		return "Sub"
	case provider.TrackDub:
		return "Dub"
	case provider.TrackDual:
		return "Dual"
	default:
		return "Auto"
	}
}

// setSubtitleLanguage writes the subtitle mode and language for mode and
// mirrors the legacy subtitle language for downloads and display.
func (m *modelImpl) setSubtitleLanguage(mode provider.ContentType, raw string) {
	prefs := m.modePreferences(mode)
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" || strings.EqualFold(trimmed, "off") {
		prefs.Subtitles.Mode = filter.SubtitleOff
		prefs.Subtitles.Language = ""
	} else {
		prefs.Subtitles.Mode = filter.SubtitlePreferred
		prefs.Subtitles.Language = lang.Normalize(trimmed)
	}
	m.setModePreferences(mode, prefs)
	m.syncLegacySubtitleFields()
}

// cycleSubtitleLanguage steps the active mode through the curated language
// list, which begins with "off".
func (m *modelImpl) cycleSubtitleLanguage(mode provider.ContentType, delta int) {
	options := lang.SubtitleOptions
	if len(options) == 0 {
		return
	}
	current := subtitleOptionIndex(prefsSubtitleCode(m.modePreferences(mode).Subtitles))
	m.setSubtitleLanguage(mode, options[(current+delta+len(options))%len(options)])
}

// prefsSubtitleCode returns the legacy code represented by a preference.
func prefsSubtitleCode(pref filter.SubtitlePreference) string {
	switch pref.Mode {
	case filter.SubtitleOff:
		return "off"
	case filter.SubtitlePreferred:
		return pref.Language
	default:
		return ""
	}
}

// subtitleOptionIndex resolves a code to its index in the curated options.
func subtitleOptionIndex(code string) int {
	for i, option := range lang.SubtitleOptions {
		if lang.Normalize(option) == lang.Normalize(code) {
			return i
		}
	}
	return 0
}

// toggleAnimeSubtitles switches the anime policy between Off and Preferred
// with burned-in subtitles enabled. The anime subtitle row no longer owns a
// separate boolean.
func (m *modelImpl) toggleAnimeSubtitles() {
	prefs := m.modePreferences(provider.ModeAnime)
	if prefs.Subtitles.Mode == filter.SubtitleOff {
		prefs.Subtitles.Mode = filter.SubtitlePreferred
		prefs.Subtitles.PreferHard = true
	} else {
		prefs.Subtitles.Mode = filter.SubtitleOff
		prefs.Subtitles.PreferHard = false
	}
	m.setModePreferences(provider.ModeAnime, prefs)
	m.syncLegacySubtitleFields()
	m.invalidateSubtitleSync()
	if m.subtitleService != nil {
		m.subtitleService.SetDisableAnimeSubtitles(m.disableAnimeSubtitles)
	}
}

// setPreferredProvider writes the resolver preference for mode and keeps the
// legacy per-mode map in sync for persistence and display.
func (m *modelImpl) setPreferredProvider(mode provider.ContentType, name string) {
	prefs := m.modePreferences(mode)
	prefs.PreferredProvider = name
	m.setModePreferences(mode, prefs)
	if m.preferredProviders == nil {
		m.preferredProviders = make(map[string]string)
	}
	if name == "" {
		delete(m.preferredProviders, string(mode))
	} else {
		m.preferredProviders[string(mode)] = name
	}
}

// cyclePreferredProvider steps the active mode through the registered
// resolver options (including Auto).
func (m *modelImpl) cyclePreferredProvider(mode provider.ContentType, delta int) {
	options := m.preferredProviderOptions(mode)
	if len(options) == 0 {
		return
	}
	current := m.preferredProviderForMode(mode)
	index := 0
	for i, option := range options {
		if option.ID == current {
			index = i
			break
		}
	}
	m.setPreferredProvider(mode, options[(index+delta+len(options))%len(options)].ID)
}

// legacyQuality maps a typed quality mode back to its persisted integer.
func legacyQuality(q filter.QualityMode) int {
	switch q {
	case filter.QualityHighest:
		return qualityHighest
	case filter.QualityDataSaver:
		return qualityDataSaver
	case filter.QualityLowest:
		return qualityLowest
	case filter.QualityUHD:
		return 10
	case filter.QualityFHD:
		return 11
	case filter.QualityHD:
		return 12
	case filter.QualitySD:
		return 13
	default:
		return qualityAll
	}
}

// qualitySettingLabel renders the display name of a quality mode in Settings.
func qualitySettingLabel(q filter.QualityMode) string {
	switch q {
	case filter.QualityAll:
		return "All qualities"
	case filter.QualityHighest:
		return "Highest"
	case filter.QualityDataSaver:
		return "Data Saver"
	case filter.QualityLowest:
		return "Lowest"
	case filter.QualityUHD:
		return "2160p"
	case filter.QualityFHD:
		return "1080p"
	case filter.QualityHD:
		return "720p"
	case filter.QualitySD:
		return "480p"
	default:
		return "All qualities"
	}
}

// syncLegacyFilterFields rebuilds every legacy filter field from the typed
// scope. saveSettings calls it so a downgraded client reads values that agree
// with the authoritative scope.
func (m *modelImpl) syncLegacyFilterFields() {
	m.qualityMode = legacyQuality(m.modePreferences(m.appMode).Quality)
	m.syncLegacyLanguageFilter()
	m.syncLegacySubtitleFields()
	m.audioMode = trackToAudioMode(m.filterScope.For(provider.ModeAnime).Track)

	if m.preferredProviders == nil {
		m.preferredProviders = make(map[string]string)
	}
	m.preferredProviders = make(map[string]string, len(m.filterScope.ByMode))
	for mode, prefs := range m.filterScope.ByMode {
		if prefs.PreferredProvider != "" {
			m.preferredProviders[string(mode)] = prefs.PreferredProvider
		}
	}
}

// syncLegacyLanguageFilter mirrors the global audio preference into the
// dual-keyed legacy map consumed by downloads and older settings files.
func (m *modelImpl) syncLegacyLanguageFilter() {
	pref := m.filterScope.Defaults.Audio
	out := make(map[string]bool, len(pref.Enabled)+len(pref.Disabled))
	for _, code := range pref.Enabled {
		out[code] = true
	}
	for code := range pref.Disabled {
		out[code] = false
	}
	m.languageFilter = out
}

// syncLegacySubtitleFields mirrors the default subtitle policy and the anime
// subtitle mode into the legacy persisted fields.
func (m *modelImpl) syncLegacySubtitleFields() {
	m.subtitleLanguage = prefsSubtitleCode(m.modePreferences(m.appMode).Subtitles)
	m.disableAnimeSubtitles = m.filterScope.For(provider.ModeAnime).Subtitles.Mode == filter.SubtitleOff
}
