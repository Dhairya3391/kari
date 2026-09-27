package settings

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"kari/internal/logging"
	"kari/internal/util"
)

// DefaultModes is the canonical default order of content modes.
var DefaultModes = []string{
	"anime",
	"cartoon",
	"live",
	"manga",
	"movies",
	"tv",
	"jellyfin",
}

// settingsLog scopes settings persistence logs.
var settingsLog = logging.With("component", "settings")

// Data is the persisted user-settings document (settings.json). Fields are
// additive and zero-value friendly so older files never need migration.
type Data struct {
	QualityMode      int             `json:"quality_mode"`
	DownloadQuality  int             `json:"download_quality"`
	LanguageFilter   map[string]bool `json:"language_filter"`
	SubtitleLanguage string          `json:"subtitle_language"`
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

	// DefaultMode sets which mode to open on startup: "last" (default), "first", or a specific mode key ("anime", "movies", etc.).
	DefaultMode string `json:"default_mode"`
	// Modes is the user-configured ordered list of mode keys.
	Modes []string `json:"modes"`
	// ModesDisabled is the list of disabled mode keys.
	ModesDisabled []string `json:"modes_disabled"`
	// LastMode is the last active content mode key.
	LastMode string `json:"last_mode"`
	// Transitions controls whether theme color crossfades are enabled (default true).
	Transitions *bool `json:"transitions"`
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

// TransitionsEnabled returns whether transitions are active (default true).
func (d *Data) TransitionsEnabled() bool {
	if d == nil || d.Transitions == nil {
		return true
	}
	return *d.Transitions
}

// path resolves the settings file location, warning when the home
// directory can't be determined.
func path() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		home = os.Getenv("HOME")
	}
	if home == "" {
		// os.UserHomeDir errors or comes back empty if HOME/USERPROFILE
		// aren't set, which does happen on Windows (e.g. some service
		// contexts). Falling through with an empty home would silently
		// resolve to a relative ".config/kari/settings.json" under
		// whatever the current working directory happens to be, so
		// settings would appear to save but never be found again on the
		// next run from a different directory.
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
			logging.Warn("settings read failed", "path", path(), "err", err)
		}
		return nil
	}
	var s Data
	if err := json.Unmarshal(data, &s); err != nil {
		logging.Warn("settings parse failed", "path", path(), "err", err)
		return nil
	}
	return &s
}

// Save persists settings atomically; failures are logged, not returned,
// since the TUI treats saving as fire-and-forget.
func Save(s *Data) {
	dir := filepath.Dir(path())
	if err := os.MkdirAll(dir, 0755); err != nil {
		logging.Warn("settings dir create failed", "dir", dir, "err", err)
		return
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		logging.Warn("settings marshal failed", "err", err)
		return
	}
	if err := util.AtomicWriteFile(path(), data, 0644); err != nil {
		logging.Warn("settings write failed", "path", path(), "err", err)
	}
}
