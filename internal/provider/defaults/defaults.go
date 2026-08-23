package defaults

import (
	"kari/internal/config"
	"kari/internal/logging"
	"kari/internal/provider"
	"kari/internal/provider/jellyfin"
	"kari/internal/provider/miruro"
	"kari/internal/provider/moviebox"
	"kari/internal/provider/piratex"
	"kari/internal/provider/rivestream"
	"kari/internal/provider/vidking"
	"kari/internal/tmdb"
)

// DefaultProviders is the single list of providers Kari knows about. Each
// entry declares its own enable gate and construction; adding a provider
// here is the only registration step — the TUI and services pick up its
// modes, features, languages, and aliases automatically.
var DefaultProviders = []provider.Descriptor{
	{
		ID: "moviebox",
		Factory: func(d provider.Deps) (provider.Provider, error) {
			return moviebox.NewClient(d.KeyPool)
		},
	},
	{
		ID: "vidking",
		Factory: func(d provider.Deps) (provider.Provider, error) {
			return vidking.NewClient(d.KeyPool)
		},
	},
	{
		ID: "rivestream",
		Factory: func(d provider.Deps) (provider.Provider, error) {
			return rivestream.NewClient(d.KeyPool)
		},
	},
	{
		ID: "miruro",
		Factory: func(d provider.Deps) (provider.Provider, error) {
			return miruro.NewClient()
		},
	},
	{
		ID: "piratex",
		Factory: func(d provider.Deps) (provider.Provider, error) {
			return piratex.NewClient()
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
// continues with the remaining integrations.
func NewDefaultRegistry(keyPool *tmdb.KeyPool, cfg *config.Config) (*provider.Registry, error) {
	registry := &provider.Registry{}
	for _, d := range DefaultProviders {
		if d.When != nil && !d.When(cfg) {
			logging.Debug("provider disabled by configuration", "provider", d.ID)
			continue
		}
		p, err := d.Factory(provider.Deps{Config: cfg, KeyPool: keyPool})
		if err != nil {
			logging.Error("provider construction failed; skipping registration", "provider", d.ID, "err", err)
			continue
		}
		registry.Register(p)
	}
	return registry, nil
}
