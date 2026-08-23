package settings

import (
	"encoding/json"
	"os"
	"path/filepath"

	"kari/internal/logging"
	"kari/internal/util"
)

// settingsLog scopes settings persistence logs.
var settingsLog = logging.With("component", "settings")

// Data is the persisted user-settings document (settings.json). Fields are
// additive and zero-value friendly so older files never need migration.
type Data struct {
	QualityMode      int             `json:"quality_mode"`
	LanguageFilter   map[string]bool `json:"language_filter"`
	SubtitleLanguage string          `json:"subtitle_language"`
	// DisableImages defaults to false (zero value) so image rendering stays
	// on for both fresh configs and existing settings.json files saved
	// before this option existed.
	DisableImages bool `json:"disable_images"`
	// AccentColor is a hex color string; empty means the default purple, so
	// existing settings.json files need no migration.
	AccentColor string `json:"accent_color,omitempty"`
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
