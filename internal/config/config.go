package config

import (
	"errors"
	"os"
	"strings"
)

type Config struct {
	TMDBAPIKeys         []string
	OpenSubtitlesKey    string
	OpenSubtitlesUser   string
	OpenSubtitlesPass   string
	TraktClientID       string
	TraktClientSecret   string
	AniListClientID     string
	AniListClientSecret string
	DownloadDir         string
	LogFile             string
	PreferredPlayer     string
	JellyfinURL         string
	JellyfinAPIKey      string
}

func AndroidUA() string {
	return WCOUserAgent
}

// Load reads configuration from environment variables.
func Load() (*Config, error) {
	cfg := &Config{
		TMDBAPIKeys:         append([]string(nil), DefaultTMDBAPIKeys...),
		OpenSubtitlesKey:    strings.TrimSpace(os.Getenv("OPENSUBTITLES_API_KEY")),
		OpenSubtitlesUser:   strings.TrimSpace(os.Getenv("OPENSUBTITLES_USERNAME")),
		OpenSubtitlesPass:   strings.TrimSpace(os.Getenv("OPENSUBTITLES_PASSWORD")),
		TraktClientID:       firstEnv("TRAKT_CLIENT_ID", "TRAKT_ID"),
		TraktClientSecret:   firstEnv("TRAKT_CLIENT_SECRET", "TRAKT_SECRET"),
		AniListClientID:     firstEnv("ANILIST_CLIENT_ID", "ANILIST_ID"),
		AniListClientSecret: firstEnv("ANILIST_CLIENT_SECRET", "ANILIST_SECRET"),
		DownloadDir:         strings.TrimSpace(os.Getenv("KARI_DOWNLOAD_DIR")),
		LogFile:             firstEnv("KARI_LOG_FILE"),
		PreferredPlayer:     firstEnv("KARI_PLAYER"),
		JellyfinURL:         strings.TrimSpace(os.Getenv("JELLYFIN_URL")),
		JellyfinAPIKey:      strings.TrimSpace(os.Getenv("JELLYFIN_API_KEY")),
	}

	// Apply hardcoded defaults if env vars are missing
	if cfg.TraktClientID == "" {
		cfg.TraktClientID = DefaultTraktClientID
	}
	if cfg.TraktClientSecret == "" {
		cfg.TraktClientSecret = DefaultTraktClientSecret
	}
	if cfg.AniListClientID == "" {
		cfg.AniListClientID = DefaultAniListClientID
	}
	if cfg.AniListClientSecret == "" {
		cfg.AniListClientSecret = DefaultAniListClientSecret
	}
	if cfg.DownloadDir == "" {
		cfg.DownloadDir = "./downloads"
	}

	if envKey := strings.TrimSpace(os.Getenv("TMDB_API_KEY")); envKey != "" {
		cfg.TMDBAPIKeys = []string{envKey}
	}

	openSubsSet := cfg.OpenSubtitlesKey != "" || cfg.OpenSubtitlesUser != "" || cfg.OpenSubtitlesPass != ""
	if openSubsSet {
		if cfg.OpenSubtitlesKey == "" || cfg.OpenSubtitlesUser == "" || cfg.OpenSubtitlesPass == "" {
			return nil, errors.New("OPENSUBTITLES_API_KEY, OPENSUBTITLES_USERNAME, and OPENSUBTITLES_PASSWORD must all be set")
		}
	}

	return cfg, nil
}

func firstEnv(keys ...string) string {
	for _, key := range keys {
		if v := strings.TrimSpace(os.Getenv(key)); v != "" {
			return v
		}
	}
	return ""
}
