package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"kari/internal/animeskip"
	"kari/internal/aniskip"
	"kari/internal/calendar"
	"kari/internal/config"
	"kari/internal/downloader"
	"kari/internal/history"
	"kari/internal/httpclient"
	"kari/internal/introdb"
	"kari/internal/logging"
	"kari/internal/manga"
	"kari/internal/player"
	"kari/internal/poster"
	"kari/internal/presence"
	"kari/internal/provider"
	"kari/internal/provider/defaults"
	"kari/internal/scrobble"
	"kari/internal/service"
	"kari/internal/settings"
	"kari/internal/skipdb"
	"kari/internal/timeouts"
	"kari/internal/tmdb"
	"kari/internal/tui"

	tea "github.com/charmbracelet/bubbletea"
)

// dependencies bundles every component the composition root builds. It is
// constructed once at startup and handed to the TUI; close releases the two
// components that own resources when the program exits.
type dependencies struct {
	config          *config.Config
	keyPool         *tmdb.KeyPool
	registry        *provider.Registry
	players         *player.Registry
	mediaService    *service.MediaService
	mangaService    *service.MangaService
	mangaClient     *manga.Client
	downloadService *service.DownloadService
	subtitleService *service.SubtitleService
	historyStore    *history.Store
	historyErr      error
	traktClient     *scrobble.TraktClient
	anilistClient   *scrobble.AniListClient
	posterClient    *poster.Client
	presenceClient  *presence.Client
	calendarClient  *calendar.Client
}

// newDependencies builds the whole component graph. A failure here is fatal;
// optional integrations degrade to nil and leave startup running.
func newDependencies(cfg *config.Config, saved *settings.Data, configDir string) (*dependencies, error) {
	d := &dependencies{config: cfg}

	histPath := filepath.Join(configDir, "history.json")
	d.historyStore, d.historyErr = history.NewStore(histPath)
	if d.historyErr != nil {
		logging.Error("history store init failed; continuing without history", "err", d.historyErr)
	}

	d.keyPool = tmdb.NewKeyPool(cfg.TMDBAPIKeys)
	skipClients := newSkipClients(d.keyPool, cfg)
	skipSettings := skipSettingsFrom(saved)

	registry, err := defaults.NewDefaultRegistry(d.keyPool, cfg)
	if err != nil {
		return nil, fmt.Errorf("create provider registry: %w", err)
	}
	d.registry = registry

	d.mediaService = service.NewMediaService(registry)
	if saved != nil && len(saved.DisabledProviders) > 0 {
		d.mediaService.SetDisabledProviders(saved.DisabledProviders)
	}
	d.mangaClient = manga.NewClient()
	d.mangaService = service.NewMangaService(registry, d.mangaClient)
	d.players = player.NewRegistry(cfg.PreferredPlayer, skipClients, skipSettings)
	d.subtitleService = service.NewSubtitleService(cfg)
	d.downloadService = service.NewDownloadService(cfg.DownloadDir, downloader.NewYTDLPDownloader(), d.mediaService, d.subtitleService)
	if saved != nil && saved.DisableDownloadVerification {
		d.downloadService.SetVerifyDownloads(false)
	}

	d.traktClient = scrobble.NewTraktClient(cfg.TraktClientID, cfg.TraktClientSecret)
	d.anilistClient = scrobble.NewAniListClient(cfg.AniListClientID, cfg.AniListClientSecret)
	d.posterClient = poster.NewClient(d.keyPool)
	d.calendarClient = calendar.NewClient(d.keyPool)

	d.presenceClient = newPresenceClient()
	if d.presenceClient != nil {
		d.players.SetProgressObserver(func(p player.PlaybackProgress) {
			d.presenceClient.UpdateProgress(presence.Progress{
				PositionSecs: p.PositionSecs,
				DurationSecs: p.DurationSecs,
				Paused:       p.Paused,
			})
		})
	}

	return d, nil
}

// close releases resources held for the lifetime of the program.
func (d *dependencies) close() {
	if d.presenceClient != nil {
		d.presenceClient.Close()
	}
	if d.historyStore != nil {
		d.historyStore.Close()
	}
}

// newSkipClients assembles the shared skip-timestamp HTTP client and its
// provider clients. Anime-skip is optional, so an init failure only logs.
func newSkipClients(keyPool *tmdb.KeyPool, cfg *config.Config) player.SkipClients {
	skipHTTP := httpclient.NewWithTimeout(timeouts.SkipHTTP)
	animeskipClient, err := animeskip.NewClient(skipHTTP, cfg.AnimeSkipClientID)
	if err != nil {
		logging.Warn("anime-skip client init failed; continuing without anime-skip", "err", err)
		animeskipClient = nil
	}
	return player.SkipClients{
		AniSkip:   aniskip.NewClient(skipHTTP),
		AnimeSkip: animeskipClient,
		SkipDB:    skipdb.NewClient(skipHTTP, config.DefaultSkipDBAPIKey),
		IntroDB:   introdb.NewClient(skipHTTP, config.DefaultIntroDBAPIKey),
		TMDB:      keyPool,
		HTTP:      skipHTTP,
	}
}

// skipSettingsFrom overlays persisted skip preferences on the defaults. An
// empty provider falls back to hybrid so older settings files keep working.
func skipSettingsFrom(saved *settings.Data) player.SkipSettings {
	s := player.SkipSettings{Provider: "hybrid"}
	if saved == nil {
		return s
	}
	if saved.SkipProvider != "" {
		s.Provider = saved.SkipProvider
	}
	s.AutoSkipIntro = saved.AutoSkipIntro
	s.AutoSkipEnding = saved.AutoSkipEnding
	s.SkipRecap = saved.SkipRecap
	s.SkipPreview = saved.SkipPreview
	return s
}

// newPresenceClient builds the Discord Rich Presence client, honoring the
// KARI_DISCORD_APP_ID override. A misconfigured id disables presence rather
// than failing startup; presence is a nicety, never a requirement.
func newPresenceClient() *presence.Client {
	appID := strings.TrimSpace(os.Getenv("KARI_DISCORD_APP_ID"))
	if appID == "" {
		appID = config.DefaultDiscordAppID
	}
	client, err := presence.New(appID)
	if err != nil {
		logging.Warn("discord presence disabled", "err", err)
		return nil
	}
	return client
}

// newTUIModel hands the fully wired dependencies to the TUI. The addon
// loaders close over the key pool so the TUI never imports concrete providers.
func newTUIModel(ctx context.Context, d *dependencies, query string, saved *settings.Data) tea.Model {
	return tui.NewModel(ctx, query, d.registry, d.players, d.config.DownloadDir,
		d.mediaService, d.mangaService, d.mangaClient, d.downloadService,
		d.subtitleService, d.historyStore, d.historyErr, d.traktClient,
		d.anilistClient, d.posterClient, d.presenceClient, d.calendarClient,
		Version, Commit, tui.ModelOptions{
			Settings: saved,
			AddonManifestTester: func(ctx context.Context, manifestURL string) (provider.Provider, error) {
				return defaults.LoadStremioAddon(ctx, manifestURL, d.keyPool)
			},
			StartupAddonLoader: func(ctx context.Context, manifestURL string) (provider.Provider, error) {
				return defaults.LoadConfiguredStremioAddon(ctx, manifestURL, d.keyPool)
			},
		})
}
