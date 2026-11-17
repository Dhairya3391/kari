package app

import (
	"os"
	"path/filepath"
	"time"

	"kari/internal/logging"
	"kari/internal/man"
	"kari/internal/manga"
	"kari/internal/poster"
	"kari/internal/selfupdate"
	"kari/internal/subtitles"
)

// startHousekeeping launches detached startup maintenance. Every task is
// best-effort: a failure must never delay the TUI or abort startup.
func startHousekeeping() {
	go selfupdate.CleanupOldExecutables()
	go func() {
		if _, err := man.EnsureInstalled(); err != nil {
			logging.Debug("man page ensure install", "err", err)
		}
	}()
	go func() {
		if err := manga.PruneDiskCache(); err != nil {
			logging.Debug("manga page cache prune failed", "err", err)
		}
		if err := poster.PruneDiskCache(); err != nil {
			logging.Debug("poster cache prune failed", "err", err)
		}
		if err := subtitles.PruneCacheDir(7 * 24 * time.Hour); err != nil {
			logging.Debug("subtitle cache prune failed", "err", err)
		}
	}()
}

// resolveConfigDir returns ~/.config/kari, falling back to $HOME when the
// user-home lookup fails (for example in a stripped-down container).
func resolveConfigDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		home = os.Getenv("HOME")
	}
	return filepath.Join(home, ".config", "kari")
}
