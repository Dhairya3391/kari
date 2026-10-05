package defaults

import (
	"kari/internal/config"
	"kari/internal/logging"
	"kari/internal/provider"
	"kari/internal/provider/anicine"
	"kari/internal/provider/anikoto"
	"kari/internal/provider/anilight"
	"kari/internal/provider/jellyfin"
	"kari/internal/provider/miruro"
	"kari/internal/provider/moovie"
	"kari/internal/provider/movysx"
	"kari/internal/provider/pengu"
	"kari/internal/provider/weebcentral"
	"kari/internal/tmdb"
)

// DefaultProviders is the single list of providers Kari knows about. Each
// entry declares its own enable gate and construction; adding a provider
// here is the only registration step — the TUI and services pick up its
// modes, features, languages, and aliases automatically.
var DefaultProviders = []provider.Descriptor{
	{
		ID: "anicine",
		Factory: func(d provider.Deps) (provider.Provider, error) {
			return anicine.NewClient(d.KeyPool)
		},
	},
	{
		ID: "movysx",
		Factory: func(d provider.Deps) (provider.Provider, error) {
			return movysx.NewClient(d.KeyPool)
		},
	},
	{
		ID: "anikoto",
		Factory: func(d provider.Deps) (provider.Provider, error) {
			return anikoto.NewClient()
		},
	},
	{
		ID: "anilight",
		Factory: func(d provider.Deps) (provider.Provider, error) {
			return anilight.NewClient()
		},
	},
	{
		ID: "miruro",
		Factory: func(d provider.Deps) (provider.Provider, error) {
			return miruro.NewClient()
		},
	},
	{
		ID: "moovie",
		Factory: func(d provider.Deps) (provider.Provider, error) {
			return moovie.NewClient(d.KeyPool)
		},
	},
	{
		ID: "weebcentral",
		Factory: func(d provider.Deps) (provider.Provider, error) {
			return weebcentral.NewClient()
		},
	},
	{
		ID: "pengu",
		Factory: func(d provider.Deps) (provider.Provider, error) {
			return pengu.NewClientWithLanguageFilter(d.KeyPool, d.Config.PenguAuthToken, d.LanguageFilter)
		},
	},
	{
		ID: "jellyfin",
		When: func(cfg *config.Config) bool {
			return cfg != nil && cfg.JellyfinURL != "" && cfg.JellyfinAPIKey != ""
		},
		Factory: func(d provider.Deps) (provider.Provider, error) {
			return jellyfin.NewClient(d.Config.JellyfinURL, d.Config.JellyfinAPIKey)
		},
	},
}

// NewDefaultRegistry constructs every enabled default provider and registers
// it. A factory failure skips that provider with a logged warning — startup
// continues with the remaining integrations. langFilter carries the user's
// audio-language settings (nil = everything enabled); factories that care
// receive it via provider.Deps.
func NewDefaultRegistry(keyPool *tmdb.KeyPool, cfg *config.Config, langFilter map[string]bool) (*provider.Registry, error) {
	registry := &provider.Registry{}
	for _, d := range DefaultProviders {
		if d.When != nil && !d.When(cfg) {
			logging.Debug("provider disabled by configuration", "provider", d.ID)
			continue
		}
		p, err := d.Factory(provider.Deps{Config: cfg, KeyPool: keyPool, LanguageFilter: langFilter})
		if err != nil {
			logging.Error("provider construction failed; skipping registration", "provider", d.ID, "err", err)
			continue
		}
		registry.Register(p)
	}
	return registry, nil
}
