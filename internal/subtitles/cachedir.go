package subtitles

import (
	"fmt"
	"os"
	"path/filepath"
)

func CacheDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		home = os.Getenv("HOME")
	}
	if home == "" {
		return "", fmt.Errorf("subtitles cache dir: could not determine home directory: %w", err)
	}
	dir := filepath.Join(home, ".config", "kari", "subs")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return dir, nil
}
