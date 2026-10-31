//go:build live

package live

import (
	"fmt"
	"net/url"
	"os/exec"
	"sort"
	"sync"
	"testing"
	"time"

	"kari/internal/provider"
)

// Audit tuning: six headless mpv probes in flight keeps a broadband link
// busy without tripping per-IP throttles; 15s bounds one slow CDN while
// the process-exit abort (see probeSourceOnSocket) ends dead streams in
// seconds instead of burning the whole timeout.
const (
	auditWorkers      = 6
	auditProbeTimeout = 15 * time.Second
)

// auditResult is one probed stream: played reports IPC duration evidence.
type auditResult struct {
	quality string
	backend string
	host    string
	played  bool
}

// TestPenguFullAudit probes every direct pengu source for three titles in
// mpv and reports per-backend health. Individual dead mirrors only log:
// mirror health is per-title and shifts hourly, so one dead row never
// fails the audit. The audit fails only when a title has no playable
// source at all — the signal that would justify disabling something.
func TestPenguFullAudit(t *testing.T) {
	if _, err := exec.LookPath("mpv"); err != nil {
		t.Skip("mpv not installed")
	}

	p, ok := newRegistry(t).ProviderByName("pengu")
	if !ok {
		t.Skip("pengu not registered")
	}
	episodeSource, ok := p.(provider.EpisodeSource)
	if !ok {
		t.Skip("pengu does not support playback")
	}

	titles := []struct {
		mode  provider.ContentType
		query string
	}{
		{provider.ModeMovies, "Inception"},
		{provider.ModeTV, "Breaking Bad"},
		{provider.ModeCartoon, "SpongeBob"},
	}

	for _, title := range titles {
		title := title
		t.Run(string(title.mode), func(t *testing.T) {
			ctx := ctxWithTimeout(t)
			results, err := p.Search(ctx, title.query, title.mode)
			skipOnCatalogDrift(t, "search", err)
			if len(results) == 0 {
				t.Fatal("search returned zero results without error")
			}
			pick := results[0]

			var sources []provider.MediaSource
			if pick.MediaType == provider.MediaTypeMovie {
				sources, err = episodeSource.ResolveSource(ctx, pick.ID, provider.Episode{TMDBID: pick.TMDBID})
				skipOnCatalogDrift(t, "resolve(movie)", err)
			} else {
				episodes, err := episodeSource.FetchEpisodes(ctx, pick)
				skipOnCatalogDrift(t, "episodes", err)
				if len(episodes) == 0 {
					t.Fatalf("%s: zero episodes without error", p.Name())
				}
				sources, err = episodeSource.ResolveSource(ctx, pick.ID, episodes[0])
				skipOnCatalogDrift(t, "resolve", err)
			}
			requireSources(t, p.Name(), sources)

			// Dedupe repeat stream URLs so shared mirrors are probed once
			// but every quality row still counts in the report.
			seen := map[string][]int{}
			unique := []provider.MediaSource{}
			for _, src := range provider.FilterDirectSources(sources) {
				if rows, dup := seen[src.URL]; dup {
					seen[src.URL] = append(rows, len(unique)-1)
					continue
				}
				seen[src.URL] = []int{len(unique)}
				unique = append(unique, src)
			}
			t.Logf("probing %d unique streams (%d rows)", len(unique), len(sources))

			played := auditProbeAll(t, unique)
			if played == 0 {
				t.Fatalf("no playable source for %q", pick.Title)
			}
		})
	}
}

// auditProbeAll probes every source with a bounded worker pool and logs a
// per-backend health table. It returns the count of playable streams.
func auditProbeAll(t *testing.T, sources []provider.MediaSource) int {
	t.Helper()

	type job struct {
		idx int
		src provider.MediaSource
	}
	jobs := make(chan job)
	results := make([]auditResult, len(sources))
	var wg sync.WaitGroup
	for w := 0; w < auditWorkers; w++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			sock := fmt.Sprintf("/tmp/kari-mpv-audit-%d.sock", id)
			for j := range jobs {
				host := j.src.URL
				if u, err := url.Parse(j.src.URL); err == nil && u.Host != "" {
					host = u.Host
				}
				r := auditResult{quality: j.src.Quality, backend: j.src.Backend, host: host}
				if err := probeSourceOnSocket(t, j.src, sock, auditProbeTimeout, false); err == nil {
					r.played = true
				}
				results[j.idx] = r
			}
		}(w)
	}
	for i, src := range sources {
		jobs <- job{idx: i, src: src}
	}
	close(jobs)
	wg.Wait()

	byBackend := map[string]*struct{ played, total int }{}
	played := 0
	for _, r := range results {
		b := byBackend[r.backend]
		if b == nil {
			b = &struct{ played, total int }{}
			byBackend[r.backend] = b
		}
		b.total++
		if r.played {
			b.played++
			played++
		} else {
			t.Logf("dead: %q host=%s", r.quality, r.host)
		}
	}
	backends := make([]string, 0, len(byBackend))
	for b := range byBackend {
		backends = append(backends, b)
	}
	sort.Strings(backends)
	for _, b := range backends {
		s := byBackend[b]
		t.Logf("backend %q: %d/%d play", b, s.played, s.total)
	}
	t.Logf("total: %d/%d play", played, len(results))
	return played
}
