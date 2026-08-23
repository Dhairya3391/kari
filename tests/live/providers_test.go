//go:build live

package live

import (
	"testing"

	"kari/internal/provider"
)

// TestProviderPipeline exercises every registered provider end to end with
// live data: Search → episode listing (or direct movie resolve) → source
// resolution. This is the exact path the TUI drives, run per provider so a
// single broken integration is pinpointed.
func TestProviderPipeline(t *testing.T) {
	reg := newRegistry(t)

	for mode, query := range queries {
		providers := reg.ProvidersForMode(mode)
		if len(providers) == 0 {
			continue
		}
		mode, query := mode, query
		t.Run(string(mode), func(t *testing.T) {
			seen := map[string]bool{}
			for _, p := range providers {
				if seen[p.Name()] {
					continue
				}
				seen[p.Name()] = true
				p := p
				t.Run(p.Name(), func(t *testing.T) {
					ctx := ctxWithTimeout(t)
					results, err := p.Search(ctx, query, mode)
					skipOnCatalogDrift(t, "search", err)
					if len(results) == 0 {
						t.Fatal("search returned zero results without error")
					}

					pick := results[0]
					// Results here come straight from p.Search, so
					// pick.Provider is unset (MediaService stamps it); only
					// the missing TMDB id matters for cross-provider reuse.
					if pick.TMDBID == 0 {
						t.Logf("%s: result %q carries no TMDB id; cross-provider reuse will skip it", p.Name(), pick.Title)
					}
					t.Logf("search ok: %q id=%q tmdb=%d mediaType=%q", pick.Title, pick.ID, pick.TMDBID, pick.MediaType)

					// Movies on providers that can resolve straight from the
					// search result skip the episode listing entirely — same
					// rule the TUI applies via MovieEpisodeFlow.
					var sources []provider.MediaSource
					if pick.MediaType == provider.MediaTypeMovie && !reg.RequiresEpisodeListForMovies(p.Name()) {
						sources, err = p.ResolveSource(ctx, pick.ID, provider.Episode{TMDBID: pick.TMDBID})
						skipOnCatalogDrift(t, "resolve(movie)", err)
						requireSources(t, p.Name(), sources)
						t.Logf("direct movie resolve ok: %d sources", len(sources))
						return
					}

					episodes, err := p.FetchEpisodes(ctx, pick)
					skipOnCatalogDrift(t, "episodes", err)
					if len(episodes) == 0 {
						t.Fatalf("%s: zero episodes without error", p.Name())
					}
					ep := episodes[len(episodes)-1] // last episode of last season
					t.Logf("episodes ok: count=%d resolving S%dE%d", len(episodes), ep.Season, ep.Episode)

					sources, err = p.ResolveSource(ctx, pick.ID, ep)
					skipOnCatalogDrift(t, "resolve", err)
					requireSources(t, p.Name(), sources)
					t.Logf("resolve ok: %d sources (first=%q)", len(sources), sources[0].Quality)
				})
			}
		})
	}
}

// TestRegistryCapabilitiesLive verifies that the capability surface agrees
// with what providers actually emit on real data: modes with audio-language
// support must come from AudioLanguagesSource providers, and display names
// must be present for aliased providers.
func TestRegistryCapabilitiesLive(t *testing.T) {
	reg := newRegistry(t)

	movies := reg.AudioLanguages(provider.ModeMovies)
	if len(movies) == 0 {
		t.Error("movies mode has audio-tagging providers but registry exposes no languages")
	}
	for _, l := range movies {
		if l.Code == "" || l.Display == "" {
			t.Errorf("audio language missing code/display: %+v", l)
		}
	}
	if got := reg.AudioLanguages(provider.ModeAnime); len(got) != 0 {
		t.Errorf("anime providers do not tag audio languages, got %+v", got)
	}
}
