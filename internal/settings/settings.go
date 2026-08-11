package settings

import (
	"encoding/json"
	"os"
	"path/filepath"

	"kari/internal/logging"
)

type Data struct {
	QualityMode      int             `json:"quality_mode"`
	LanguageFilter   map[string]bool `json:"language_filter"`
	SubtitleLanguage string          `json:"subtitle_language"`
	// DisableImages defaults to false (zero value) so image rendering stays
	// on for both fresh configs and existing settings.json files saved
	// before this option existed.
	DisableImages bool `json:"disable_images"`
}

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
		logging.Warnf("settings: could not determine home directory, falling back to relative path")
	}
	return filepath.Join(home, ".config", "kari", "settings.json")
}

func Load() *Data {
	data, err := os.ReadFile(path())
	if err != nil {
		if !os.IsNotExist(err) {
			logging.Warnf("settings: failed to read %s: %v", path(), err)
		}
		return nil
	}
	var s Data
	if err := json.Unmarshal(data, &s); err != nil {
		logging.Warnf("settings: failed to parse %s: %v", path(), err)
		return nil
	}
	return &s
}

func Save(s *Data) {
	dir := filepath.Dir(path())
	if err := os.MkdirAll(dir, 0755); err != nil {
		logging.Warnf("settings: failed to create %s: %v", dir, err)
		return
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		logging.Warnf("settings: failed to marshal settings: %v", err)
		return
	}
	if err := os.WriteFile(path(), data, 0644); err != nil {
		logging.Warnf("settings: failed to write %s: %v", path(), err)
	}
}
