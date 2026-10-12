//go:build live

package live

import (
	"errors"
	"net/url"
	"os"
	"os/exec"
	"sort"
	"strings"
	"testing"
	"time"

	"kari/internal/player"
	"kari/internal/provider"
	"kari/internal/service"
)

// errNoPlayback reports an mpv probe with no IPC duration evidence.
var errNoPlayback = errors.New("no IPC evidence of playback")

// mpvMatrixCase is one provider × mode combination verified against real
// mpv playback.
type mpvMatrixCase struct {
	provider string
	mode     provider.ContentType
	query    string
}

// mpvMatrixCases covers every video provider in every mode it supports,
// using the same canonical queries as the pipeline test.
var mpvMatrixCases = []mpvMatrixCase{
	{provider: "movysx", mode: provider.ModeMovies, query: "Inception"},
	{provider: "movysx", mode: provider.ModeTV, query: "Breaking Bad"},
	{provider: "movysx", mode: provider.ModeCartoon, query: "SpongeBob"},
	{provider: "moovie", mode: provider.ModeMovies, query: "Inception"},
	{provider: "moovie", mode: provider.ModeTV, query: "Breaking Bad"},
	{provider: "moovie", mode: provider.ModeCartoon, query: "SpongeBob"},
	{provider: "atlantic", mode: provider.ModeMovies, query: "Inception"},
	{provider: "atlantic", mode: provider.ModeTV, query: "Breaking Bad"},
	{provider: "atlantic", mode: provider.ModeCartoon, query: "SpongeBob"},
	{provider: "cinecat", mode: provider.ModeMovies, query: "Inception"},
	{provider: "cinecat", mode: provider.ModeTV, query: "Breaking Bad"},
	{provider: "cinecat", mode: provider.ModeCartoon, query: "SpongeBob"},
	{provider: "pengu", mode: provider.ModeMovies, query: "Inception"},
	{provider: "pengu", mode: provider.ModeTV, query: "Breaking Bad"},
	{provider: "pengu", mode: provider.ModeCartoon, query: "SpongeBob"},
	{provider: "anicine", mode: provider.ModeAnime, query: "Naruto"},
	{provider: "anikoto", mode: provider.ModeAnime, query: "Naruto"},
	{provider: "miruro", mode: provider.ModeAnime, query: "Naruto"},
	{provider: "anilight", mode: provider.ModeAnime, query: "Naruto"},
}

// maxMPVCandidates caps how many resolved sources one matrix case probes
// in mpv. Production tries every source in ranked order; probing a bounded
// prefix keeps the matrix fast while covering mirror fallbacks.
const maxMPVCandidates = 6

// TestMPVMatrixAllProviders resolves one playable stream per provider per
// mode and proves it loads in real mpv via IPC duration. Cases whose
// upstreams are down (no sources) skip like the pipeline test; transport
// errors fail. Runs sequentially so cases share the one mpv IPC socket.
func TestMPVMatrixAllProviders(t *testing.T) {
	if _, err := exec.LookPath("mpv"); err != nil {
		t.Skip("mpv not installed")
	}

	reg := newRegistry(t)
	for _, tc := range mpvMatrixCases {
		tc := tc
		t.Run(tc.provider+"/"+string(tc.mode), func(t *testing.T) {
			p, ok := reg.ProviderByName(tc.provider)
			if !ok {
				t.Skipf("%s not registered", tc.provider)
			}
			ctx := ctxWithTimeout(t)
			results, err := p.Search(ctx, tc.query, tc.mode)
			skipOnCatalogDrift(t, "search", err)
			if len(results) == 0 {
				t.Fatal("search returned zero results without error")
			}
			pick := results[0]

			var sources []provider.MediaSource
			ep := provider.Episode{TMDBID: pick.TMDBID}
			if pick.MediaType == provider.MediaTypeMovie && !reg.RequiresEpisodeListForMovies(p.Name()) {
				sources, err = p.ResolveSource(ctx, pick.ID, ep)
				skipOnCatalogDrift(t, "resolve(movie)", err)
			} else {
				episodes, err := p.FetchEpisodes(ctx, pick)
				skipOnCatalogDrift(t, "episodes", err)
				if len(episodes) == 0 {
					t.Fatalf("%s: zero episodes without error", p.Name())
				}
				ep = episodes[0]
				sources, err = p.ResolveSource(ctx, pick.ID, ep)
				skipOnCatalogDrift(t, "resolve", err)
			}
			requireSources(t, p.Name(), sources)

			playable := provider.FilterDirectSources(sources)
			if len(playable) == 0 {
				t.Skipf("%s: no direct mpv-playable URLs (%d proxied)", p.Name(), len(sources))
			}
			// Highest quality first, challenged hosts last — the order the
			// app attempts via the aggregate sort.
			sort.SliceStable(playable, func(i, j int) bool {
				ci, cj := 0, 0
				if provider.ChallengedHost(playable[i].URL) {
					ci = 1
				}
				if provider.ChallengedHost(playable[j].URL) {
					cj = 1
				}
				if ci != cj {
					return ci < cj
				}
				return service.SourceQuality(playable[i].Quality) > service.SourceQuality(playable[j].Quality)
			})

			t.Logf("resolved %d sources (%d direct); probing %q", len(sources), len(playable), playable[0].Quality)
			played := false
			var lastErr error
			for i, src := range playable {
				if i >= maxMPVCandidates {
					break
				}
				if provider.ChallengedHost(src.URL) {
					t.Logf("candidate[%d] %q is challenged-host; trying anyway", i, src.Quality)
				}
				if err := probeSourceMPV(t, src); err != nil {
					t.Logf("candidate[%d] %q failed: %v", i, src.Quality, err)
					lastErr = err
					continue
				}
				t.Logf("mpv playback ok via candidate[%d] %q", i, src.Quality)
				played = true
				break
			}
			if !played {
				t.Fatalf("no candidate played in mpv (tried up to %d): %v", maxMPVCandidates, lastErr)
			}
		})
	}
}

// probeSourceMPV launches headless mpv (null video/audio) with the source's
// production transport headers and waits up to 20s for IPC duration
// evidence that the stream demuxes. Null drivers prove URL + headers +
// demux without opening windows; the headed player path is covered by
// TestMPVPlaybackResolvesAndPlays. The process is always reaped.
func probeSourceMPV(t *testing.T, src provider.MediaSource) error {
	t.Helper()
	return probeSourceOnSocket(t, src, player.DefaultMPVSocketPath(), 20*time.Second, true)
}

// probeSourceOnSocket launches headless mpv (null video/audio) with the
// source's production transport headers and waits up to timeout for IPC
// duration evidence that the stream demuxes. Null drivers prove URL +
// headers + demux without opening windows; the headed player path is
// covered by TestMPVPlaybackResolvesAndPlays. The process is always
// reaped. Custom sockets let parallel audits run side by side.
func probeSourceOnSocket(t *testing.T, src provider.MediaSource, sock string, timeout time.Duration, verbose bool) error {
	t.Helper()
	_ = os.Remove(sock)

	args := []string{
		"--no-ytdl",
		"--really-quiet",
		"--vo=null",
		"--ao=null",
		"--network-timeout=6",
		"--hls-bitrate=max",
		"--input-ipc-server=" + sock,
	}
	if ua := strings.TrimSpace(src.UserAgent); ua != "" {
		args = append(args, "--user-agent="+ua)
	}
	if ref := strings.TrimSpace(src.Referer); ref != "" {
		args = append(args, "--referrer="+ref)
		var headers []string
		if !src.SuppressOrigin {
			if origin := originOfReferer(ref); origin != "" {
				headers = append(headers, "Origin: "+origin)
			}
		}
		if cookie := strings.TrimSpace(src.CookieHeader); cookie != "" {
			headers = append(headers, "Cookie: "+cookie)
		}
		if len(headers) > 0 {
			args = append(args, "--http-header-fields="+strings.Join(headers, ","))
		}
	} else if cookie := strings.TrimSpace(src.CookieHeader); cookie != "" {
		args = append(args, "--http-header-fields=Cookie: "+cookie)
	}
	args = append(args, src.URL)

	cmd := exec.Command("mpv", args...)
	_ = cmd.Start()
	// One waiter feeds both the early-abort poll and the reap below.
	done := make(chan struct{})
	go func() { _ = cmd.Wait(); close(done) }()
	// Always reap: quit via IPC when possible, then kill.
	defer func() {
		quitMPVSocket(t, sock, verbose)
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			_ = cmd.Process.Kill()
			<-done
		}
		_ = os.Remove(sock)
	}()

	if d := pollMVPDurationOnSocket(t, sock, timeout, verbose, done); d > 0 {
		if verbose {
			t.Logf("playback confirmed duration=%.1fs", d)
		}
		return nil
	}
	return errNoPlayback
}

// originOfReferer derives scheme://host from a referer for the Origin
// header, mirroring the production player path.
func originOfReferer(ref string) string {
	u, err := url.Parse(strings.TrimSpace(ref))
	if err != nil || u.Scheme == "" || u.Host == "" {
		return ""
	}
	return u.Scheme + "://" + u.Host
}
