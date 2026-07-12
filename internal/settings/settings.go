package settings

import (
	"encoding/json"
	"os"
	"path/filepath"
)

type Data struct {
	QualityMode    int             `json:"quality_mode"`
	LanguageFilter map[string]bool `json:"language_filter"`
}

func path() string {
	home, err := os.UserHomeDir()
	if err != nil {
		home = os.Getenv("HOME")
	}
	return filepath.Join(home, ".config", "kari", "settings.json")
}

func Load() *Data {
	data, err := os.ReadFile(path())
	if err != nil {
		return nil
	}
	var s Data
	if err := json.Unmarshal(data, &s); err != nil {
		return nil
	}
	return &s
}

func Save(s *Data) {
	dir := filepath.Dir(path())
	if err := os.MkdirAll(dir, 0755); err != nil {
		return
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return
	}
	os.WriteFile(path(), data, 0644)
}
