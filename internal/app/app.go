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
	"kari/internal/poster"
	"kari/internal/provider/defaults"
	"kari/internal/scrobble"
	"kari/internal/service"
	"kari/internal/tmdb"
	"kari/internal/tui"
)

// Version and Commit are set at build time via -ldflags (see build.sh),
// which derives Version from git tags/commit count — there's nothing to
// keep in sync here manually. The defaults below are only what a plain
// `go run`/`go build` without those ldflags will show.
var (
	Version = "0.0.0-dev"
	Commit  = "dev"
)

func getArgs() (args []string, version bool, update bool) {
	for _, arg := range os.Args[1:] {
		if arg == "-v" || arg == "--version" {
			version = true
			continue
		}
		if arg == "-u" || arg == "-U" || arg == "--update" {
			update = true
			continue
		}
		args = append(args, arg)
	}
	return args, version, update
}

// Run boots the whole application: parse args, load config, wire every
// component in internal/provider/defaults and app, then hand control to the
// bubbletea program. It is the only place concrete components meet.
func Run() error {
	args, showVersion, showUpdate := getArgs()
	if showVersion {
		fmt.Printf("Kari version %s (%s)\n", Version, Commit)
		return nil
	}
	if showUpdate {
		return Update()
	}
	query := strings.TrimSpace(strings.Join(args, " "))
	logging.Info("starting app", "query", query)
	if p := logging.Path(); p != "" {
		logging.Info("log file ready", "path", p)
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
	historyStore, historyErr := history.NewStore(histPath)
	if historyErr != nil {
		logging.Error("history store init failed; continuing without history", "err", historyErr)
	}

	keyPool := tmdb.NewKeyPool(cfg.TMDBAPIKeys)
	aniskipClient := aniskip.NewClient(httpclient.NewWithTimeout(10 * time.Second))

	registry, err := defaults.NewDefaultRegistry(keyPool, cfg)
	if err != nil {
		return err
	}

	players := player.NewRegistry(cfg.PreferredPlayer, aniskipClient)
	mediaService := service.NewMediaService(registry)
	downloadService := service.NewDownloadService(cfg.DownloadDir, downloader.NewYTDLPDownloader(), mediaService)
	subtitleService := service.NewSubtitleService(cfg)

	traktClient := scrobble.NewTraktClient(cfg.TraktClientID, cfg.TraktClientSecret)
	anilistClient := scrobble.NewAniListClient(cfg.AniListClientID, cfg.AniListClientSecret)
	posterClient := poster.NewClient(keyPool)

	m := tui.NewModel(context.Background(), query, registry, players, cfg.DownloadDir, mediaService, downloadService, subtitleService, historyStore, historyErr, traktClient, anilistClient, posterClient, Version)
	p := tea.NewProgram(m, tea.WithAltScreen())
	_, err = p.Run()
	if err != nil {
		logging.Error("program exited with error", "err", err)
	}
	if historyStore != nil {
		historyStore.Close()
	}
	return err
}
