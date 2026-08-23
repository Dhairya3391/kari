package subtitles

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// CacheDir returns (creating if needed) the directory where downloaded
// subtitle files are materialized for player consumption.
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

// PruneCacheDir deletes cached subtitle files older than maxAge. Every
// download (provider, OpenSubtitles) writes a new file here and
// nothing else ever removes them, so without this the directory grows
// without bound over a long-lived install. Safe to call while playback is
// in progress: an in-use subtitle file was just written, so it's always
// far younger than maxAge.
func PruneCacheDir(maxAge time.Duration) error {
	dir, err := CacheDir()
	if err != nil {
		return err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	cutoff := time.Now().Add(-maxAge)
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		if info.ModTime().Before(cutoff) {
			_ = os.Remove(filepath.Join(dir, entry.Name()))
		}
	}
	return nil
}
