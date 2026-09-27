package app

import (
	"os"
	"path/filepath"

	"kari/internal/logging"
)

// migrateConfigDir reorganizes ~/.config/kari into a clean directory structure:
//   - tokens/ (anilist.json, trakt.json, opensubtitles.json)
//   - cache/ (posters/, manga/, subs/)
//   - root files: history.json, history.json.bak, settings.json, kari.log
//
// Legacy files and directories are safely moved into their organized locations.
func migrateConfigDir(configDir string) {
	if configDir == "" {
		return
	}
	if _, err := os.Stat(configDir); os.IsNotExist(err) {
		return
	}

	tokensDir := filepath.Join(configDir, "tokens")
	cacheDir := filepath.Join(configDir, "cache")

	// Token file migrations
	tokenMigrations := []struct {
		legacyName string
		newName    string
	}{
		{"anilist_token.json", "anilist.json"},
		{"trakt_token.json", "trakt.json"},
		{"os_token.json", "opensubtitles.json"},
	}

	for _, tm := range tokenMigrations {
		oldPath := filepath.Join(configDir, tm.legacyName)
		newPath := filepath.Join(tokensDir, tm.newName)

		if _, err := os.Stat(oldPath); err == nil {
			if _, err := os.Stat(newPath); os.IsNotExist(err) {
				_ = os.MkdirAll(tokensDir, 0o755)
				if err := os.Rename(oldPath, newPath); err != nil {
					logging.Debug("migrate token file failed", "from", oldPath, "to", newPath, "err", err)
				} else {
					logging.Debug("migrated token file", "from", tm.legacyName, "to", filepath.Join("tokens", tm.newName))
				}
			} else {
				// New path already exists, clean up duplicate legacy file
				_ = os.Remove(oldPath)
			}
		}
	}

	// Cache directory migrations
	cacheMigrations := []struct {
		legacyDir string
		newDir    string
	}{
		{"posters", filepath.Join(cacheDir, "posters")},
		{"manga", filepath.Join(cacheDir, "manga")},
		{"subs", filepath.Join(cacheDir, "subs")},
	}

	for _, cm := range cacheMigrations {
		oldDir := filepath.Join(configDir, cm.legacyDir)
		newDir := cm.newDir

		if _, err := os.Stat(oldDir); err == nil {
			if _, err := os.Stat(newDir); os.IsNotExist(err) {
				_ = os.MkdirAll(cacheDir, 0o755)
				if err := os.Rename(oldDir, newDir); err != nil {
					logging.Debug("migrate cache dir failed", "from", oldDir, "to", newDir, "err", err)
				} else {
					logging.Debug("migrated cache directory", "from", cm.legacyDir, "to", cm.newDir)
				}
			}
		}
	}
}
