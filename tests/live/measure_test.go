//go:build live && measure

package live

import (
	"testing"
	"time"

	"kari/internal/provider"
)

// TestMeasure records the natural latency of each pipeline stage per provider.
// Run it with:
//
//	go test -tags "live measure" ./tests/live -run TestMeasure -v
//
// The durations it prints are the "measured P95" inputs for the budgets in
// internal/timeouts. Repeat a few times and use the high end of each range.
func TestMeasure(t *testing.T) {
	reg := newRegistry(t)

	type sample struct {
		provider string
		stage    string
		duration time.Duration
		err      error
	}
	var samples []sample
	record := func(name, stage string, start time.Time, err error) {
		samples = append(samples, sample{name, stage, time.Since(start), err})
	}

	for mode, query := range queries {
		providers := reg.ProvidersForMode(mode)
		seen := map[string]bool{}
		for _, p := range providers {
			if seen[p.Name()] {
				continue
			}
			seen[p.Name()] = true
			es, ok := p.(provider.EpisodeSource)
			if !ok {
				continue
			}
			t.Run(string(mode)+"/"+p.Name(), func(t *testing.T) {
				ctx := ctxWithTimeout(t)

				start := time.Now()
				results, err := p.Search(ctx, query, mode)
				record(p.Name(), "search", start, err)
				if err != nil || len(results) == 0 {
					t.Logf("search skipped: %v", err)
					return
				}
				pick := results[0]

				var ep provider.Episode
				if pick.MediaType != provider.MediaTypeMovie || reg.RequiresEpisodeListForMovies(p.Name()) {
					start = time.Now()
					eps, err := es.FetchEpisodes(ctx, pick)
					record(p.Name(), "episodes", start, err)
					if err != nil || len(eps) == 0 {
						t.Logf("episodes skipped: %v", err)
						return
					}
					ep = eps[0]
				} else {
					ep = provider.Episode{TMDBID: pick.TMDBID}
					if mode == provider.ModeAnime {
						ep.Audio = provider.AudioSub
					}
				}

				start = time.Now()
				_, err = es.ResolveSource(ctx, pick.ID, ep)
				record(p.Name(), "resolve", start, err)
			})
		}
	}

	t.Log("=== measured P95 inputs (ms) ===")
	for _, s := range samples {
		t.Logf("%-18s %-10s %8dms  err=%v", s.provider, s.stage, s.duration.Milliseconds(), s.err)
	}
}
