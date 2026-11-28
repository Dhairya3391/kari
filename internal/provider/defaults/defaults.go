package defaults

import (
	"context"
	"strings"

	"kari/internal/config"
	"kari/internal/logging"
	"kari/internal/provider"
	"kari/internal/provider/a11"
	"kari/internal/provider/anidream"
	"kari/internal/provider/atsu"
	"kari/internal/provider/bingr"
	"kari/internal/provider/cinecat"
	"kari/internal/provider/cinejoy"
	"kari/internal/provider/cno"
	"kari/internal/provider/famelack"
	"kari/internal/provider/jellyfin"
	"kari/internal/provider/miruro"
	"kari/internal/provider/movysx"
	"kari/internal/provider/pengu"
	"kari/internal/provider/solis"
	"kari/internal/provider/stremioaddon"
	"kari/internal/provider/vidgold"
	"kari/internal/provider/vidora"
	"kari/internal/provider/vidrift"
	"kari/internal/provider/vumeto"
	"kari/internal/provider/vyla"
	"kari/internal/provider/way2movies"
	"kari/internal/provider/weebcentral"
	"kari/internal/tmdb"
)

// DefaultProviders is the single list of providers Kari knows about. Each
// entry declares its own enable gate and construction; adding a provider
// here is the only registration step — the TUI and services pick up its
// modes, features, languages, and aliases automatically.
var DefaultProviders = []provider.Descriptor{
	{
		ID: "movysx",
		Factory: func(d provider.Deps) (provider.Provider, error) {
			return movysx.NewClient(d.KeyPool)
		},
	},
	{
		ID: "miruro",
		Factory: func(d provider.Deps) (provider.Provider, error) {
			return miruro.NewClient()
		},
	},
	{
		ID: "pengu",
		Factory: func(d provider.Deps) (provider.Provider, error) {
			return pengu.NewClient(d.KeyPool)
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
		ID: "cinejoy",
		Factory: func(d provider.Deps) (provider.Provider, error) {
			return cinejoy.NewClient(d.KeyPool)
		},
	},
	{
		ID:        "vidgold",
		LegacyIDs: []string{"sweetfox"},
		Factory: func(d provider.Deps) (provider.Provider, error) {
			return vidgold.NewClient(d.KeyPool)
		},
	},
	{
		ID: "cno",
		Factory: func(d provider.Deps) (provider.Provider, error) {
			return cno.NewClient(d.KeyPool)
		},
	},
	{
		ID: "vidora",
		Factory: func(d provider.Deps) (provider.Provider, error) {
			return vidora.NewClient(d.KeyPool)
		},
	},
	{
		ID: "vidrift",
		Factory: func(d provider.Deps) (provider.Provider, error) {
			return vidrift.NewClient(d.KeyPool)
		},
	},
	{
		ID: "vumeto",
		Factory: func(d provider.Deps) (provider.Provider, error) {
			return vumeto.NewClient()
		},
	},
	{
		ID: "bingr",
		Factory: func(d provider.Deps) (provider.Provider, error) {
			return bingr.NewClient(d.KeyPool)
		},
	},
	{
		ID: "anidream",
		Factory: func(d provider.Deps) (provider.Provider, error) {
			return anidream.NewClient()
		},
	},
	{
		ID: "weebcentral",
		Factory: func(d provider.Deps) (provider.Provider, error) {
			return weebcentral.NewClient()
		},
	},
	{
		ID: "famelack",
		Factory: func(d provider.Deps) (provider.Provider, error) {
			return famelack.NewClient()
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
	{
		ID: "a11",
		Factory: func(d provider.Deps) (provider.Provider, error) {
			return a11.NewClient(d.KeyPool)
		},
	},
	{
		ID: "vyla",
		Factory: func(d provider.Deps) (provider.Provider, error) {
			return vyla.NewClient(d.KeyPool)
		},
	},
	{
		ID: "atsu",
		Factory: func(d provider.Deps) (provider.Provider, error) {
			return atsu.NewClient()
		},
	},
	{
		ID: "way2movies",
		Factory: func(d provider.Deps) (provider.Provider, error) {
			return way2movies.NewClient(d.KeyPool)
		},
	},
}

// ProviderIDRenames maps each retired provider ID to the current ID that
// replaced it, derived from the descriptors' LegacyIDs. Settings migration
// reads this map so renaming a provider only requires declaring its old IDs
// alongside the new one — the migration logic never hard-codes a name.
func ProviderIDRenames() map[string]string {
	renames := make(map[string]string)
	for _, d := range DefaultProviders {
		for _, legacy := range d.LegacyIDs {
			if id := strings.ToLower(strings.TrimSpace(legacy)); id != "" {
				renames[id] = d.ID
			}
		}
	}
	return renames
}

// NewDefaultRegistry constructs every enabled default provider and registers
// it. A factory failure skips that provider with a logged warning — startup
// continues with the remaining integrations. Audio-language preferences are
// applied once by the domain filter pipeline, never by providers themselves.
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

// LoadConfiguredStremioAddon rebuilds a previously configured addon from its
// manifest URL.
func LoadConfiguredStremioAddon(ctx context.Context, manifestURL string, keyPool *tmdb.KeyPool) (provider.Provider, error) {
	return stremioaddon.NewClient(ctx, manifestURL, keyPool)
}

// LoadStremioAddon loads and validates the HTTP manifest of a Stremio addon
// before making it available to Kari. It does not run a playback probe.
func LoadStremioAddon(ctx context.Context, manifestURL string, keyPool *tmdb.KeyPool) (provider.Provider, error) {
	return stremioaddon.NewClient(ctx, manifestURL, keyPool)
}
