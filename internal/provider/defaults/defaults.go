package defaults

import (
	"kari/internal/config"
	"kari/internal/logging"
	"kari/internal/provider"
	"kari/internal/provider/anicine"
	"kari/internal/provider/anikoto"
	"kari/internal/provider/anipm"
	"kari/internal/provider/anivault"
	"kari/internal/provider/bingr"
	"kari/internal/provider/cinecat"
	"kari/internal/provider/cno"
	"kari/internal/provider/jellyfin"
	"kari/internal/provider/mapple"
	"kari/internal/provider/miruro"
	"kari/internal/provider/moovie"
	"kari/internal/provider/movysx"
	"kari/internal/provider/pengu"
	"kari/internal/provider/solis"
	"kari/internal/provider/vidrift"
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
			return anicine.NewClient()
		},
	},
	{
		ID: "movysx",
		Factory: func(d provider.Deps) (provider.Provider, error) {
			return movysx.NewClientWithLanguageFilter(d.KeyPool, d.LanguageFilter)
		},
	},
	{
		ID: "anikoto",
		Factory: func(d provider.Deps) (provider.Provider, error) {
			return anikoto.NewClient()
		},
	},
	{
		ID: "anipm",
		Factory: func(d provider.Deps) (provider.Provider, error) {
			return anipm.NewClient()
		},
	},
	{
		ID: "anivault",
		Factory: func(d provider.Deps) (provider.Provider, error) {
			return anivault.NewClient()
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
			return moovie.NewClientWithLanguageFilter(d.KeyPool, d.LanguageFilter)
		},
	},
	{
		ID: "solis",
		Factory: func(d provider.Deps) (provider.Provider, error) {
			return solis.NewClient(d.KeyPool)
		},
	},
	{
		ID: "cinecat",
		Factory: func(d provider.Deps) (provider.Provider, error) {
			return cinecat.NewClient(d.KeyPool)
		},
	},
	{
		ID: "cno",
		Factory: func(d provider.Deps) (provider.Provider, error) {
			return cno.NewClient(d.KeyPool)
		},
	},
	{
		ID: "mapple",
		Factory: func(d provider.Deps) (provider.Provider, error) {
			return mapple.NewClient(d.KeyPool)
		},
	},
	{
		ID: "vidrift",
		Factory: func(d provider.Deps) (provider.Provider, error) {
			return vidrift.NewClientWithLanguageFilter(d.KeyPool, d.LanguageFilter)
		},
	},
	{
		ID: "bingr",
		Factory: func(d provider.Deps) (provider.Provider, error) {
			return bingr.NewClientWithLanguageFilter(d.KeyPool, d.LanguageFilter)
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
