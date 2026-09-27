package app

import (
	"context"
	"fmt"
	"image/color"
	"os"
	"path/filepath"
	"strings"
	"time"

	"kari/internal/animeskip"
	"kari/internal/aniskip"
	"kari/internal/config"
	"kari/internal/introdb"
	"kari/internal/downloader"
	"kari/internal/history"
	"kari/internal/httpclient"
	"kari/internal/logging"
	"kari/internal/manga"
	"kari/internal/player"
	"kari/internal/poster"
	"kari/internal/provider/defaults"
	"kari/internal/skipdb"
	"kari/internal/scrobble"
	"kari/internal/service"
	"kari/internal/settings"
	"kari/internal/subtitles"
	"kari/internal/tmdb"
	"kari/internal/tui"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/muesli/termenv"
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
	configDir := filepath.Join(home, ".config", "kari")
	migrateConfigDir(configDir)

	histPath := filepath.Join(configDir, "history.json")
	historyStore, historyErr := history.NewStore(histPath)
	if historyErr != nil {
		logging.Error("history store init failed; continuing without history", "err", historyErr)
	}

	keyPool := tmdb.NewKeyPool(cfg.TMDBAPIKeys)
	skipHTTP := httpclient.NewWithTimeout(10 * time.Second)
	aniskipClient := aniskip.NewClient(skipHTTP)
	animeskipClient, err := animeskip.NewClient(skipHTTP, cfg.AnimeSkipClientID)
	if err != nil {
		logging.Warn("anime-skip client init failed; continuing without anime-skip", "err", err)
		animeskipClient = nil
	}
	skipdbClient := skipdb.NewClient(skipHTTP, config.DefaultSkipDBAPIKey)
	introdbClient := introdb.NewClient(skipHTTP, config.DefaultIntroDBAPIKey)
	skipClients := player.SkipClients{
		AniSkip:   aniskipClient,
		AnimeSkip: animeskipClient,
		SkipDB:    skipdbClient,
		IntroDB:   introdbClient,
		TMDB:      keyPool,
		HTTP:      skipHTTP,
	}

	skipSettings := player.SkipSettings{
		Provider: "hybrid",
	}
	savedSettings := settings.Load()
	if savedSettings != nil {
		if savedSettings.SkipProvider != "" {
			skipSettings.Provider = savedSettings.SkipProvider
		}
		skipSettings.AutoSkipIntro = savedSettings.AutoSkipIntro
		skipSettings.AutoSkipEnding = savedSettings.AutoSkipEnding
		skipSettings.SkipRecap = savedSettings.SkipRecap
		skipSettings.SkipPreview = savedSettings.SkipPreview
	}

	// Audio-language filter for providers (nil = everything enabled).
	var langFilter map[string]bool
	if savedSettings != nil {
		langFilter = savedSettings.LanguageFilter
	}

	registry, err := defaults.NewDefaultRegistry(keyPool, cfg, langFilter)
	if err != nil {
		return err
	}
	mediaService := service.NewMediaService(registry)
	mangaService := service.NewMangaService(registry)
	mangaClient := manga.NewClient()
	players := player.NewRegistry(cfg.PreferredPlayer, skipClients, skipSettings)
	downloadService := service.NewDownloadService(cfg.DownloadDir, downloader.NewYTDLPDownloader(), mediaService)
	subtitleService := service.NewSubtitleService(cfg)

	traktClient := scrobble.NewTraktClient(cfg.TraktClientID, cfg.TraktClientSecret)
	anilistClient := scrobble.NewAniListClient(cfg.AniListClientID, cfg.AniListClientSecret)
	posterClient := poster.NewClient(keyPool)

	// Bound on-disk caches so repeat sessions don't grow them without limit;
	// eviction is oldest-first and failures only log.
	if err := manga.PruneDiskCache(); err != nil {
		logging.Debug("manga page cache prune failed", "err", err)
	}
	if err := poster.PruneDiskCache(); err != nil {
		logging.Debug("poster cache prune failed", "err", err)
	}
	if err := subtitles.PruneCacheDir(7 * 24 * time.Hour); err != nil {
		logging.Debug("subtitle cache prune failed", "err", err)
	}

	bg := detectTerminalBackground()
	m := tui.NewModel(context.Background(), query, registry, players, cfg.DownloadDir, mediaService, mangaService, mangaClient, downloadService, subtitleService, historyStore, historyErr, traktClient, anilistClient, posterClient, Version)
	if setter, ok := m.(interface{ SetBaseBackgroundColor(color.RGBA) }); ok {
		setter.SetBaseBackgroundColor(bg)
	}
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

func detectTerminalBackground() color.RGBA {
	darkFallback := tui.HexToRGBA("#0B0D10")
	lightFallback := tui.HexToRGBA("#FAFAFA")

	ch := make(chan termenv.Color, 1)
	go func() {
		c := termenv.BackgroundColor()
		ch <- c
	}()

	select {
	case c := <-ch:
		if rgb, ok := c.(termenv.RGBColor); ok && string(rgb) != "" {
			return tui.HexToRGBA(string(rgb))
		}
	case <-time.After(150 * time.Millisecond):
	}

	if termenv.HasDarkBackground() {
		return darkFallback
	}
	return lightFallback
}
