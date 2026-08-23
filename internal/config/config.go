package config

import (
	"errors"
	"os"
	"strings"
)

// Config is the resolved application configuration: environment overrides
// layered over defaults from constants.go. Constructed once in app.Run and
// passed down to every component that needs it.
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

// AndroidUA returns the shared Android browser User-Agent constant. It
// exists as a function so callers never import the raw const directly and
// platform-specific wrappers stay in one place.
func AndroidUA() string {
	return AndroidUAConst
}

// Load reads configuration from environment variables, applying hardcoded
// defaults where variables are unset, and validates that partial
// credential sets (e.g. OpenSubtitles) are either complete or absent.
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

// firstEnv returns the first non-empty value among the given environment
// variable names, supporting legacy aliases alongside current names.
func firstEnv(keys ...string) string {
	for _, key := range keys {
		if v := strings.TrimSpace(os.Getenv(key)); v != "" {
			return v
		}
	}
	return ""
}
