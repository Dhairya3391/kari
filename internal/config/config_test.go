package config

import (
	"path/filepath"
	"testing"
)

func TestLoadUsesUserDownloadsDirectory(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("KARI_DOWNLOAD_DIR", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := filepath.Join(home, "Downloads")
	if cfg.DownloadDir != want {
		t.Fatalf("DownloadDir = %q, want %q", cfg.DownloadDir, want)
	}
}

func TestLoadHonorsDownloadDirectoryOverride(t *testing.T) {
	want := t.TempDir()
	t.Setenv("KARI_DOWNLOAD_DIR", want)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.DownloadDir != want {
		t.Fatalf("DownloadDir = %q, want %q", cfg.DownloadDir, want)
	}
}
