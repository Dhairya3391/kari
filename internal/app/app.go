package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/charmbracelet/bubbletea"

	"kari/internal/aniskip"
	"kari/internal/config"
	"kari/internal/downloader"
	"kari/internal/history"
	"kari/internal/httpclient"
	"kari/internal/logging"
	"kari/internal/player"
	"kari/internal/provider/defaults"
	"kari/internal/provider/jellyfin"
	"kari/internal/scrobble"
	"kari/internal/service"
	"kari/internal/tmdb"
	"kari/internal/tui"
)

func Run() error {
	args, showVersion, showUpdate := getArgs()
	if showVersion {
		fmt.Printf("Kari version %s (%s)\n", Version, Commit)
		return nil
	}
	if showUpdate {
		return Update()
	}
	BackgroundUpdate()
	query := strings.TrimSpace(strings.Join(args, " "))
	logging.Infof("starting app query=%q", query)
	if p := logging.Path(); p != "" {
		logging.Infof("debug log path: %s", p)
	}

	cfg, err := config.Load()
	if err != nil {
		return err
	}

	home, err := os.UserHomeDir()
	if err != nil {
		home = os.Getenv("HOME")
	}
	histPath := filepath.Join(home, ".config", "kari", "history.json")
	historyStore, err := history.NewStore(histPath)
	if err != nil {
		logging.Errorf("failed to initialize history store: %v", err)
	}

	keyPool := tmdb.NewKeyPool(cfg.TMDBAPIKeys)
	aniskipClient := aniskip.NewClient(httpclient.NewWithTimeout(10 * time.Second))

	registry, err := defaults.NewDefaultRegistry(keyPool)
	if err != nil {
		return err
	}

	if cfg.JellyfinURL != "" && cfg.JellyfinAPIKey != "" {
		jf, err := jellyfin.NewClient(cfg.JellyfinURL, cfg.JellyfinAPIKey)
		if err != nil {
			logging.Errorf("failed to create jellyfin provider: %v", err)
		} else {
			registry.Register(jf)
			logging.Infof("jellyfin provider registered (server=%s)", cfg.JellyfinURL)
		}
	}

	players := player.NewRegistry(cfg.PreferredPlayer, aniskipClient)
	mediaService := service.NewMediaService(registry)
	downloadService := service.NewDownloadService(cfg.DownloadDir, []downloader.Downloader{downloader.NewWCODownloader(), downloader.NewMiruroDownloader(), downloader.NewYTDLPDownloader()})
	subtitleService := service.NewSubtitleService(cfg)

	traktClient := scrobble.NewTraktClient(cfg.TraktClientID, cfg.TraktClientSecret)
	anilistClient := scrobble.NewAniListClient(cfg.AniListClientID, cfg.AniListClientSecret)

	m := tui.NewModel(context.Background(), query, true, registry, players, cfg.DownloadDir, mediaService, downloadService, subtitleService, historyStore, traktClient, anilistClient)
	p := tea.NewProgram(m, tea.WithAltScreen())
	_, err = p.Run()
	if err != nil {
		logging.Errorf("program exited with error: %v", err)
	}
	return err
}
