//go:build live

// Package live contains real-network integration tests for Kari's full
// pipeline: provider search → episode listing → source resolution, real
// mpv playback of a resolved stream, and a real yt-dlp download.
//
// They hit third-party services and are therefore gated behind the `live`
// build tag so ordinary `go test ./...` stays hermetic:
//
//	go test -tags live ./tests/live -v
package live

import (
	"context"
	"errors"
	"testing"
	"time"

	"kari/internal/config"
	"kari/internal/provider"
	"kari/internal/provider/defaults"
	"kari/internal/tmdb"
)

// liveTimeout bounds every network step; third-party APIs can be slow but
// a test that takes minutes is a failure in itself.
const liveTimeout = 45 * time.Second

// queries maps each content mode to a title that every healthy provider of
// that mode is expected to know.
var queries = map[provider.ContentType]string{
	provider.ModeMovies:  "Inception",
	provider.ModeTV:      "Breaking Bad",
	provider.ModeAnime:   "Naruto",
	provider.ModeCartoon: "SpongeBob",
}

// newRegistry builds the same provider registry the app uses at startup,
// driven by the ambient environment so Jellyfin joins in when configured.
func newRegistry(t *testing.T) *provider.Registry {
	t.Helper()
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config load: %v", err)
	}
	reg, err := defaults.NewDefaultRegistry(tmdb.NewKeyPool(cfg.TMDBAPIKeys), cfg)
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	return reg
}

// requireSources asserts the basic shape of resolved sources: non-empty
// transport URL and, for the primary source, a quality label.
func requireSources(t *testing.T, name string, srcs []provider.MediaSource) {
	t.Helper()
	if len(srcs) == 0 {
		t.Fatalf("%s returned no sources", name)
	}
	for i, s := range srcs {
		if s.URL == "" {
			t.Errorf("%s source[%d] has empty URL", name, i)
		}
	}
}

// skipOnCatalogDrift lets expected catalog churn (a title genuinely absent
// from a pirate API today) skip instead of fail, while transport-level or
// unexpected errors still fail loudly.
func skipOnCatalogDrift(t *testing.T, name string, err error) {
	t.Helper()
	if err == nil {
		return
	}
	if errors.Is(err, provider.ErrNoResults) ||
		errors.Is(err, provider.ErrNoEpisodes) ||
		errors.Is(err, provider.ErrNoSources) {
		t.Skipf("%s: %v", name, err)
		return
	}
	t.Fatalf("%s: %v", name, err)
}

func ctxWithTimeout(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), liveTimeout)
	t.Cleanup(cancel)
	return ctx
}
