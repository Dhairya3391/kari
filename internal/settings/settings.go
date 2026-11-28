package settings

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"kari/internal/filter"
	"kari/internal/logging"
	"kari/internal/provider"
	"kari/internal/provider/defaults"
	"kari/internal/util"
)

// DefaultModes is the canonical default order of content modes.
var DefaultModes = []string{
	string(provider.ModeAnime),
	string(provider.ModeCartoon),
	string(provider.ModeStremio),
	string(provider.ModeLive),
	string(provider.ModeManga),
	string(provider.ModeMovies),
	string(provider.ModeTV),
	string(provider.ModeJellyfin),
}

// settingsLog scopes settings persistence logs.
var settingsLog = logging.With("component", "settings")

// Data is the persisted user-settings document (settings.json). Fields are
// additive and zero-value friendly; provider IDs renamed in past releases are
// remapped on load so older files keep their preferences.
type Data struct {
	QualityMode     int `json:"quality_mode"`
	DownloadQuality int `json:"download_quality"`
	// PreferredProviders maps content modes to their preferred playback and download provider.
	PreferredProviders map[string]string `json:"preferred_providers,omitempty"`
	// DisableDownloadVerification turns off post-download video integrity checks.
	DisableDownloadVerification bool            `json:"disable_download_verification,omitempty"`
	LanguageFilter              map[string]bool `json:"language_filter"`
	SubtitleLanguage            string          `json:"subtitle_language"`
	// DisableAnimeSubtitles suppresses subtitles specifically for anime content,
	// while keeping them active in the preferred language for movies and TV.
	DisableAnimeSubtitles bool `json:"disable_anime_subtitles"`
	// DefaultAnimeAudio sets the startup audio track for anime: "sub" (default) or "dub".
	// DefaultAnimeAudio sets the startup audio track for anime: "sub" (default) or "dub".
	DefaultAnimeAudio string `json:"default_anime_audio"`
	// AnimeTitleLanguage sets the display title language for anime: "romaji" (default) or "english".
	AnimeTitleLanguage string `json:"anime_title_language"`
	// PreferredPlayer sets the default player name (e.g. "mpv", "iina", "vlc").
	PreferredPlayer string `json:"preferred_player"`
	// Autoplay enables automatic playback transition to the next episode upon completion.
	Autoplay bool `json:"autoplay"`
	// DisableImages defaults to false (zero value) so image rendering stays
	// on for both fresh configs and existing settings.json files.
	DisableImages bool `json:"disable_images"`
	// AccentColor is a hex color string or preset name; "auto" adapts per content mode.
	AccentColor string `json:"accent_color"`
	// SkipProvider determines which service resolves OP/ED/recap/preview skip
	// boundaries: "hybrid" (default if empty), "skipdb", "introdb", "anime-skip", "aniskip", or "off".
	SkipProvider string `json:"skip_provider"`
	// AutoSkipIntro automatically seeks past the opening without prompting.
	AutoSkipIntro bool `json:"auto_skip_intro"`
	// AutoSkipEnding automatically seeks past the ending without prompting.
	AutoSkipEnding bool `json:"auto_skip_ending"`
	// SkipRecap automatically seeks past detected recap segments.
	SkipRecap bool `json:"skip_recap"`
	// SkipPreview automatically seeks past detected preview segments.
	SkipPreview bool `json:"skip_preview"`
	// StartupSync controls whether watched history is synced from AniList/Trakt on startup.
	StartupSync bool `json:"startup_sync"`
	// DisableHistory stops saving local playback progress for every mode.
	DisableHistory bool `json:"disable_history,omitempty"`
	// HistoryModesDisabled lists modes whose playback progress is not saved.
	HistoryModesDisabled []string `json:"history_modes_disabled,omitempty"`
	// DiscordPresence publishes playback as Discord Rich Presence. Off by
	// default so nothing leaves the machine unless the user opts in.
	DiscordPresence bool `json:"discord_presence"`

	// DefaultMode sets which mode to open on startup: "last" (default), "first", or a specific mode key ("anime", "movies", etc.).
	DefaultMode string `json:"default_mode"`
	// Modes is the user-configured ordered list of mode keys.
	Modes []string `json:"modes"`
	// ModesDisabled is the list of disabled mode keys.
	ModesDisabled []string `json:"modes_disabled"`
	// DisabledProviders is the list of user-disabled provider keys (e.g. "miruro", "cinecat").
	// When empty/nil, all providers are enabled.
	DisabledProviders []string `json:"disabled_providers,omitempty"`
	// StremioAddonManifests stores user-configured Stremio-compatible addon manifest URLs.
	StremioAddonManifests []string `json:"stremio_addon_manifests,omitempty"`
	// LastMode is the last active content mode key.
	LastMode string `json:"last_mode"`
	// Transitions controls whether theme color crossfades are enabled (default true).
	Transitions *bool `json:"transitions"`
	// StartupSearchActive controls whether Kari starts with the search input focused (default true).
	StartupSearchActive *bool `json:"startup_search_active"`
	// AutoUpdate controls whether Kari automatically updates in the background when a new version is available (default false).
	AutoUpdate bool `json:"auto_update"`

	// Filters is the typed per-mode filtering policy. Once migrated it is the
	// single source of truth for quality, audio-language, subtitle and track
	// preferences; the legacy scalar fields above are kept only for older
	// settings files and for downgrade compatibility.
	Filters filter.Scope `json:"filters,omitempty"`
	// FiltersMigrated records that the legacy fields have been folded into
	// Filters, so an all-default policy is not re-seeded on every load.
	FiltersMigrated bool `json:"filters_migrated,omitempty"`
}

// EffectiveFilters returns the typed per-mode filtering policy, seeding it
// from the legacy scalar fields on first use. The seeded scope is stored back
// on the document so every later read and save uses the same single source.
func (d *Data) EffectiveFilters() filter.Scope {
	if d == nil {
		return filter.FromLegacy(0, 0, nil, "", "", false, "")
	}
	if d.FiltersMigrated {
		return d.Filters
	}

	scope := filter.FromLegacy(
		d.QualityMode,
		d.DownloadQuality,
		d.LanguageFilter,
		d.SubtitleLanguage,
		d.DefaultAnimeAudio,
		d.DisableAnimeSubtitles,
		"",
	)
	if scope.ByMode == nil {
		scope.ByMode = make(map[provider.ContentType]filter.Preferences)
	}
	for mode, name := range d.PreferredProviders {
		modePrefs := scope.For(provider.ContentType(mode))
		modePrefs.PreferredProvider = name
		scope.ByMode[provider.ContentType(mode)] = modePrefs
	}
	d.Filters = scope
	d.FiltersMigrated = true
	return scope
}

// DisabledHistoryModeSet returns a set of modes excluded from local history.
func (d *Data) DisabledHistoryModeSet() map[string]bool {
	set := make(map[string]bool)
	if d == nil {
		return set
	}
	for _, raw := range d.HistoryModesDisabled {
		mode := strings.ToLower(strings.TrimSpace(raw))
		if mode != "" {
			set[mode] = true
		}
	}
	return set
}

// NormalizedModes returns the ordered list of known modes, preserving the user's
// order, appending any newly added known modes, and ignoring unknown keys.
func (d *Data) NormalizedModes() []string {
	knownMap := make(map[string]bool)
	for _, m := range DefaultModes {
		knownMap[m] = true
	}

	seen := make(map[string]bool)
	var ordered []string

	if d != nil {
		for _, raw := range d.Modes {
			m := strings.ToLower(strings.TrimSpace(raw))
			if knownMap[m] && !seen[m] {
				ordered = append(ordered, m)
				seen[m] = true
			}
		}
	}

	// Append any known modes not in user's list (e.g. newly added modes)
	for _, m := range DefaultModes {
		if !seen[m] {
			ordered = append(ordered, m)
			seen[m] = true
		}
	}

	return ordered
}

// DisabledModeSet returns a set of disabled mode keys.
func (d *Data) DisabledModeSet() map[string]bool {
	set := make(map[string]bool)
	if d == nil {
		return set
	}
	for _, raw := range d.ModesDisabled {
		m := strings.ToLower(strings.TrimSpace(raw))
		if m != "" {
			set[m] = true
		}
	}
	return set
}

// DisabledProviderSet returns a set of disabled provider keys (lowercased).
func (d *Data) DisabledProviderSet() map[string]bool {
	set := make(map[string]bool)
	if d == nil {
		return set
	}
	for _, raw := range d.DisabledProviders {
		p := strings.ToLower(strings.TrimSpace(raw))
		if p != "" {
			set[p] = true
		}
	}
	return set
}

// migrateLegacyProviderIDs remaps retired provider IDs to their current IDs.
// The rename map comes from provider descriptors, so a renamed provider only
// needs to declare its old IDs next to its new one — this code stays unchanged.
func (d *Data) migrateLegacyProviderIDs(renames map[string]string) {
	if d == nil {
		return
	}
	for i, raw := range d.DisabledProviders {
		d.DisabledProviders[i] = remapProviderID(raw, renames)
	}
	for mode, name := range d.PreferredProviders {
		d.PreferredProviders[mode] = remapProviderID(name, renames)
	}
	if d.FiltersMigrated {
		d.Filters.Defaults.PreferredProvider = remapProviderID(d.Filters.Defaults.PreferredProvider, renames)
		for mode, prefs := range d.Filters.ByMode {
			prefs.PreferredProvider = remapProviderID(prefs.PreferredProvider, renames)
			d.Filters.ByMode[mode] = prefs
		}
	}
}

// remapProviderID replaces a retired provider ID with its current ID, keeping
// any mode-scoped prefix (e.g. "anime:oldid") intact. Unknown IDs pass through
// unchanged.
func remapProviderID(raw string, renames map[string]string) string {
	id := strings.TrimSpace(raw)
	prefix := ""
	if i := strings.LastIndex(id, ":"); i >= 0 {
		prefix, id = id[:i+1], strings.TrimSpace(id[i+1:])
	}
	current, ok := renames[strings.ToLower(id)]
	if !ok {
		return raw
	}
	return prefix + current
}

// TransitionsEnabled returns whether transitions are active (default true).
func (d *Data) TransitionsEnabled() bool {
	if d == nil || d.Transitions == nil {
		return true
	}
	return *d.Transitions
}

// StartupSearchActiveEnabled returns whether the search input is focused on startup (default true).
func (d *Data) StartupSearchActiveEnabled() bool {
	if d == nil || d.StartupSearchActive == nil {
		return true
	}
	return *d.StartupSearchActive
}

// path resolves the settings file location, warning when the home
// directory can't be determined.
func path() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		home = os.Getenv("HOME")
	}
	if home == "" {
		settingsLog.Warn("home directory undetermined; using relative path fallback")
	}
	return filepath.Join(home, ".config", "kari", "settings.json")
}

// Load reads settings.json, returning nil when it's missing or unreadable
// (callers fall back to defaults). Corrupt files are warned about, never
// fatal — a broken settings.json must not stop the app from launching.
func Load() *Data {
	data, err := os.ReadFile(path())
	if err != nil {
		if !os.IsNotExist(err) {
			settingsLog.Warn("settings read failed", "path", path(), "err", err)
		}
		return nil
	}
	var s Data
	if err := json.Unmarshal(data, &s); err != nil {
		settingsLog.Warn("settings parse failed", "path", path(), "err", err)
		return nil
	}
	s.migrateLegacyProviderIDs(defaults.ProviderIDRenames())
	return &s
}

// Save persists settings atomically; failures are logged, not returned,
// since the TUI treats saving as fire-and-forget.
func Save(s *Data) {
	dir := filepath.Dir(path())
	if err := os.MkdirAll(dir, 0700); err != nil {
		settingsLog.Warn("settings dir create failed", "dir", dir, "err", err)
		return
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		settingsLog.Warn("settings marshal failed", "err", err)
		return
	}
	if err := util.AtomicWriteFile(path(), data, 0600); err != nil {
		settingsLog.Warn("settings write failed", "path", path(), "err", err)
	}
}
