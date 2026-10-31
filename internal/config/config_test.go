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

func TestLoadUsesBundledCredentials(t *testing.T) {
	for _, key := range []string{
		"TRAKT_CLIENT_ID", "TRAKT_ID",
		"TRAKT_CLIENT_SECRET", "TRAKT_SECRET",
		"ANILIST_CLIENT_ID", "ANILIST_ID",
		"ANILIST_CLIENT_SECRET", "ANILIST_SECRET",
		"KARI_ANIME_SKIP_CLIENT_ID",
		"PENGU_AUTH_TOKEN", "PENGU_TOKEN",
	} {
		t.Setenv(key, "must-be-ignored")
	}

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.TraktClientID != DefaultTraktClientID {
		t.Errorf("TraktClientID = %q, want bundled default", cfg.TraktClientID)
	}
	if cfg.TraktClientSecret != DefaultTraktClientSecret {
		t.Errorf("TraktClientSecret = %q, want bundled default", cfg.TraktClientSecret)
	}
	if cfg.AniListClientID != DefaultAniListClientID {
		t.Errorf("AniListClientID = %q, want bundled default", cfg.AniListClientID)
	}
	if cfg.AniListClientSecret != DefaultAniListClientSecret {
		t.Errorf("AniListClientSecret = %q, want bundled default", cfg.AniListClientSecret)
	}
	if cfg.AnimeSkipClientID != DefaultAnimeSkipClientID {
		t.Errorf("AnimeSkipClientID = %q, want bundled default", cfg.AnimeSkipClientID)
	}
	if cfg.PenguAuthToken != DefaultPenguAuthToken {
		t.Errorf("PenguAuthToken = %q, want bundled default", cfg.PenguAuthToken)
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
