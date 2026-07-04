package defaults

import (
	"kari/internal/provider"
	"kari/internal/provider/miruro"
	"kari/internal/provider/vidking"
	"kari/internal/provider/vidnest"
	"kari/internal/provider/wco"
	"kari/internal/tmdb"
)

var DefaultProviders = []provider.Descriptor{
	{
		ID: "vidnest",
		Factory: func(kp *tmdb.KeyPool) (provider.Provider, error) {
			return vidnest.NewClient(kp)
		},
		Modes: []provider.Mode{
			{Name: provider.ModeMovies, Priority: 1},
			{Name: provider.ModeTV, Priority: 2},
		},
		Priority: 1,
	},
	{
		ID: "vidking",
		Factory: func(kp *tmdb.KeyPool) (provider.Provider, error) {
			return vidking.NewClient(kp)
		},
		Modes: []provider.Mode{
			{Name: provider.ModeMovies, Priority: 2},
			{Name: provider.ModeTV, Priority: 1},
		},
		Priority: 2,
	},
	{
		ID: "miruro",
		Factory: func(kp *tmdb.KeyPool) (provider.Provider, error) {
			return miruro.NewClient()
		},
		Modes: []provider.Mode{
			{Name: provider.ModeAnime, Priority: 1},
		},
		Priority: 1,
	},
	{
		ID: "wco",
		Factory: func(kp *tmdb.KeyPool) (provider.Provider, error) {
			return wco.NewClient("")
		},
		Modes: []provider.Mode{
			{Name: provider.ModeCartoon, Priority: 1},
		},
		Priority: 2,
	},
}

func NewDefaultRegistry(keyPool *tmdb.KeyPool) (*provider.Registry, error) {
	registry := &provider.Registry{}
	for _, d := range DefaultProviders {
		p, err := d.Factory(keyPool)
		if err != nil {
			return nil, err
		}
		registry.Register(p)
	}
	return registry, nil
}
