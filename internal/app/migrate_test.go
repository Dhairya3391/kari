package app

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMigrateConfigDir(t *testing.T) {
	tempDir := t.TempDir()
	configDir := filepath.Join(tempDir, ".config", "kari")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	// Create legacy token files
	legacyAnilist := filepath.Join(configDir, "anilist_token.json")
	legacyTrakt := filepath.Join(configDir, "trakt_token.json")
	legacyOS := filepath.Join(configDir, "os_token.json")

	_ = os.WriteFile(legacyAnilist, []byte(`{"access_token":"ani123"}`), 0o600)
	_ = os.WriteFile(legacyTrakt, []byte(`{"access_token":"trakt123"}`), 0o600)
	_ = os.WriteFile(legacyOS, []byte(`{"token":"os123"}`), 0o600)

	// Create legacy cache directories with dummy files
	legacyPosters := filepath.Join(configDir, "posters")
	legacyManga := filepath.Join(configDir, "manga")
	legacySubs := filepath.Join(configDir, "subs")

	_ = os.MkdirAll(legacyPosters, 0o755)
	_ = os.MkdirAll(legacyManga, 0o755)
	_ = os.MkdirAll(legacySubs, 0o755)

	_ = os.WriteFile(filepath.Join(legacyPosters, "test.jpg"), []byte("poster"), 0o644)
	_ = os.WriteFile(filepath.Join(legacyManga, "page.img"), []byte("page"), 0o644)
	_ = os.WriteFile(filepath.Join(legacySubs, "sub.srt"), []byte("sub"), 0o644)

	// Keep core files
	_ = os.WriteFile(filepath.Join(configDir, "history.json"), []byte("[]"), 0o644)
	_ = os.WriteFile(filepath.Join(configDir, "settings.json"), []byte("{}"), 0o644)
	_ = os.WriteFile(filepath.Join(configDir, "kari.log"), []byte("log"), 0o644)

	// Run migration
	migrateConfigDir(configDir)

	// Verify tokens migrated
	newAnilist := filepath.Join(configDir, "tokens", "anilist.json")
	newTrakt := filepath.Join(configDir, "tokens", "trakt.json")
	newOS := filepath.Join(configDir, "tokens", "opensubtitles.json")

	if _, err := os.Stat(newAnilist); err != nil {
		t.Errorf("expected %s to exist: %v", newAnilist, err)
	}
	if _, err := os.Stat(newTrakt); err != nil {
		t.Errorf("expected %s to exist: %v", newTrakt, err)
	}
	if _, err := os.Stat(newOS); err != nil {
		t.Errorf("expected %s to exist: %v", newOS, err)
	}

	// Verify legacy token files removed
	if _, err := os.Stat(legacyAnilist); err == nil {
		t.Errorf("expected legacy file %s to be moved", legacyAnilist)
	}
	if _, err := os.Stat(legacyTrakt); err == nil {
		t.Errorf("expected legacy file %s to be moved", legacyTrakt)
	}
	if _, err := os.Stat(legacyOS); err == nil {
		t.Errorf("expected legacy file %s to be moved", legacyOS)
	}

	// Verify cache dirs migrated
	newPosters := filepath.Join(configDir, "cache", "posters", "test.jpg")
	newManga := filepath.Join(configDir, "cache", "manga", "page.img")
	newSubs := filepath.Join(configDir, "cache", "subs", "sub.srt")

	if _, err := os.Stat(newPosters); err != nil {
		t.Errorf("expected poster in cache: %v", err)
	}
	if _, err := os.Stat(newManga); err != nil {
		t.Errorf("expected manga page in cache: %v", err)
	}
	if _, err := os.Stat(newSubs); err != nil {
		t.Errorf("expected subtitle in cache: %v", err)
	}

	// Verify core files untouched
	if _, err := os.Stat(filepath.Join(configDir, "history.json")); err != nil {
		t.Errorf("history.json should stay in root: %v", err)
	}
	if _, err := os.Stat(filepath.Join(configDir, "settings.json")); err != nil {
		t.Errorf("settings.json should stay in root: %v", err)
	}
	if _, err := os.Stat(filepath.Join(configDir, "kari.log")); err != nil {
		t.Errorf("kari.log should stay in root: %v", err)
	}
}
