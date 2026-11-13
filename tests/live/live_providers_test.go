//go:build live

package live

import (
	"fmt"
	"math"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"kari/internal/player"
	"kari/internal/provider"
)

// TestLiveProviderPipeline exercises every live provider end to end: search,
// resolve, and reachability verification of the resolved sources. Live
// catalogs carry many dead mirrors, so a provider passes as long as at least
// one resolved source verifies for at least one sampled channel.
func TestLiveProviderPipeline(t *testing.T) {
	reg := newRegistry(t)

	for _, p := range reg.ProvidersForMode(provider.ModeLive) {
		p := p
		t.Run(p.Name(), func(t *testing.T) {
			episodeSource, ok := p.(provider.EpisodeSource)
			if !ok {
				t.Skip("provider does not support playback")
			}
			ctx := ctxWithTimeout(t)

			results, err := p.Search(ctx, "", provider.ModeLive)
			skipOnCatalogDrift(t, "search", err)
			if len(results) == 0 {
				t.Fatal("search returned zero results without error")
			}
			t.Logf("search ok: %d channels", len(results))

			const sampleSize = 5
			if len(results) > sampleSize {
				results = results[:sampleSize]
			}
			verified := 0
			for _, pick := range results {
				episodes, err := episodeSource.FetchEpisodes(ctx, pick)
				skipOnCatalogDrift(t, "episodes", err)
				ep := provider.Episode{}
				if len(episodes) > 0 {
					ep = episodes[0]
				}
				sources, err := episodeSource.ResolveSource(ctx, pick.ID, ep)
				skipOnCatalogDrift(t, "resolve", err)
				requireSources(t, p.Name(), sources)
				t.Logf("%q: %d sources", pick.Title, len(sources))

				for i, src := range sources {
					if src.Type == "" {
						t.Errorf("%q source[%d] has empty type", pick.Title, i)
					}
					if verifier, ok := p.(provider.LiveStreamVerifier); ok {
						if err := verifier.VerifyLiveStream(ctx, provider.ModeLive, src); err != nil {
							t.Logf("%q source[%d] (%s) unverified: %v", pick.Title, i, src.URL, err)
							continue
						}
						verified++
						break
					}
				}
			}
			if _, ok := p.(provider.LiveStreamVerifier); ok && verified == 0 {
				t.Errorf("no sampled source verified as playable")
			}
		})
	}
}

// TestLiveProviderPlayback opens the first verified source of each live
// provider in a real headless mpv and requires evidence that media actually
// loaded (duration or advancing position over IPC).
func TestLiveProviderPlayback(t *testing.T) {
	if _, err := exec.LookPath("mpv"); err != nil {
		t.Skip("mpv not installed")
	}
	reg := newRegistry(t)

	for _, p := range reg.ProvidersForMode(provider.ModeLive) {
		p := p
		t.Run(p.Name(), func(t *testing.T) {
			episodeSource, ok := p.(provider.EpisodeSource)
			if !ok {
				t.Skip("provider does not support playback")
			}
			ctx := ctxWithTimeout(t)

			results, err := p.Search(ctx, "", provider.ModeLive)
			skipOnCatalogDrift(t, "search", err)

			var (
				chosen provider.MediaSource
				title  string
			)
			for _, pick := range results {
				episodes, epErr := episodeSource.FetchEpisodes(ctx, pick)
				if epErr != nil {
					continue
				}
				ep := provider.Episode{}
				if len(episodes) > 0 {
					ep = episodes[0]
				}
				sources, resolveErr := episodeSource.ResolveSource(ctx, pick.ID, ep)
				if resolveErr != nil {
					continue
				}
				for _, src := range sources {
					if verifier, ok := p.(provider.LiveStreamVerifier); ok {
						if err := verifier.VerifyLiveStream(ctx, provider.ModeLive, src); err != nil {
							continue
						}
					}
					chosen, title = src, pick.Title
					break
				}
				if chosen.URL != "" {
					break
				}
			}
			if chosen.URL == "" {
				t.Skip("no verified source in the sampled catalog")
			}
			t.Logf("playing %q: %s", title, chosen.URL)
			probeLiveSourceMPV(t, chosen)
		})
	}
}

// probeLiveSourceMPV launches headless mpv with the source's production
// transport headers and waits for live playback evidence: a positive
// duration or a position that advances past the first second. Live HLS
// often reports no duration at all, so time-pos is the primary witness.
func probeLiveSourceMPV(t *testing.T, src provider.MediaSource) {
	t.Helper()
	sock := fmt.Sprintf("/tmp/kari-live-test-%d.sock", time.Now().UnixNano())
	_ = os.Remove(sock)

	args := []string{
		"--no-ytdl",
		"--msg-level=all=warn",
		"--vo=null",
		"--ao=null",
		"--network-timeout=8",
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
	}
	args = append(args, src.URL)

	cmd := exec.Command("mpv", args...)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start mpv: %v", err)
	}
	done := make(chan struct{})
	var waitErr error
	go func() { waitErr = cmd.Wait(); close(done) }()
	defer func() {
		quitMPVSocket(t, sock, false)
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			_ = cmd.Process.Kill()
			<-done
		}
		_ = os.Remove(sock)
	}()

	if pos := pollMPVPosition(t, sock, 25*time.Second, done); pos > 1 {
		t.Logf("playback confirmed position=%.1fs", pos)
		return
	}
	t.Fatalf("no IPC evidence of live playback; mpv exit=%v stderr: %s", waitErr, stderr.String())
}

// pollMPVPosition polls one mpv IPC socket until time-pos advances past one
// second, duration becomes positive, or the process exits.
func pollMPVPosition(t *testing.T, sock string, within time.Duration, abort <-chan struct{}) float64 {
	t.Helper()
	deadline := time.Now().Add(within)
	var lastPos float64
	for time.Now().Before(deadline) {
		select {
		case <-abort:
			return lastPos
		default:
		}
		c := player.NewIPCClient(sock)
		if err := c.Connect(time.Second); err == nil {
			if v, err := c.GetProperty("time-pos"); err == nil {
				if pos, ok := v.(float64); ok {
					lastPos = pos
					if pos > 1 {
						_ = c.Close()
						return pos
					}
				}
			}
			if v, err := c.GetProperty("duration"); err == nil {
				if dur, ok := v.(float64); ok && dur > 0 {
					_ = c.Close()
					return math.Max(lastPos, 2)
				}
			}
			_ = c.Close()
		}
		time.Sleep(250 * time.Millisecond)
	}
	return lastPos
}
