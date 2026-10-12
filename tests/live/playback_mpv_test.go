//go:build live

package live

import (
	"net"
	"os"
	"os/exec"
	"testing"
	"time"

	"kari/internal/model"
	"kari/internal/player"
	"kari/internal/provider"
	"kari/internal/service"
)

// TestMPVPlaybackResolvesAndPlays drives the real production path:
// MediaService.Resolve against live providers, then player.Registry
// launching desktop mpv with the exact args the app uses. It polls the mpv
// IPC socket for a real duration value, proving the resolved stream is
// genuinely playable end to end (network + headers + demux).
func TestMPVPlaybackResolvesAndPlays(t *testing.T) {
	if _, err := exec.LookPath("mpv"); err != nil {
		t.Skip("mpv not installed")
	}

	reg := newRegistry(t)
	svc := service.NewMediaService(reg)
	ctx := ctxWithTimeout(t)

	results, _, _, err := svc.Search(ctx, provider.ModeMovies, queries[provider.ModeMovies])
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("search returned zero results")
	}
	pick := results[0]
	t.Logf("playing %q via %q", pick.Title, pick.Provider)

	resolved, err := svc.Resolve(ctx, provider.ModeMovies, pick, provider.Episode{}, nil, service.ResolveOptions{})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if len(resolved.Playback) == 0 {
		t.Fatal("resolve produced no playback sources")
	}

	// Lowest quality starts fastest — we're proving playability, not
	// benchmarking 4K startup.
	lowest := service.FilterPlaybackSources(resolved.Playback, 3 /* lowest */, nil)
	if len(lowest) == 0 {
		lowest = resolved.Playback
	}
	resolved.Playback = lowest
	t.Logf("resolved %d sources; attempting playback of %q", len(lowest), lowest[0].Quality)

	// Clear any stale socket from a previous crashed run.
	_ = os.Remove(player.DefaultMPVSocketPath())

	// Whatever happens below — assertion failure, timeout, panic — never
	// leave an orphaned mpv playing on this machine.
	t.Cleanup(func() {
		quitMPV(t)
		time.Sleep(500 * time.Millisecond)
	})

	players := player.NewRegistry("mpv", player.SkipClients{}, player.SkipSettings{Provider: "off"})
	type playOutcome struct {
		result player.PlaybackResult
		err    error
	}
	done := make(chan playOutcome, 1)
	go func() {
		result, err := players.PlayWithSources(resolved.Playback, resolved, "mpv")
		done <- playOutcome{result: result, err: err}
	}()

	// Proof of playback comes from two independent witnesses: our own IPC
	// polling here, and the production telemetry the player gathered. Either
	// is sufficient.
	extDuration := pollMVPDuration(t, 60*time.Second)
	if extDuration > 0 {
		t.Logf("playback confirmed via external IPC poll duration=%.1fs", extDuration)
	} else {
		select {
		case out := <-done:
			if out.err != nil {
				t.Fatalf("player exited without playing: %v", out.err)
			}
			if out.result.DurationSecs <= 0 && out.result.FinalPositionSecs <= 0 {
				t.Fatal("player exited cleanly but reported zero playback progress")
			}
			t.Logf("playback confirmed via player telemetry pos=%.1fs dur=%.1fs",
				out.result.FinalPositionSecs, out.result.DurationSecs)
		default:
			t.Fatalf("no IPC evidence of playback after 60s at %s", player.DefaultMPVSocketPath())
		}
	}

	// Tell mpv to quit; PlayWithSources then returns on its own.
	quitMPV(t)

	select {
	case out := <-done:
		t.Logf("player returned after quit err=%v pos=%.1fs", out.err, out.result.FinalPositionSecs)
	case <-time.After(15 * time.Second):
		t.Log("player goroutine did not return within 15s of quit; continuing")
	}
}

// TestCinecatMPVPlayback resolves a title directly via the Cinecat provider
// and verifies that mpv loads, demuxes, and plays the stream via IPC confirmation.
func TestCinecatMPVPlayback(t *testing.T) {
	if _, err := exec.LookPath("mpv"); err != nil {
		t.Skip("mpv not installed")
	}
	client, ok := newRegistry(t).ProviderByName("cinecat")
	if !ok {
		t.Fatal("cinecat provider not registered")
	}

	ctx := ctxWithTimeout(t)
	sources, err := client.ResolveSource(ctx, "27205", provider.Episode{TMDBID: 27205})
	if err != nil {
		t.Fatalf("cinecat resolve: %v", err)
	}
	if len(sources) == 0 {
		t.Fatal("cinecat produced zero sources")
	}

	t.Logf("cinecat resolved %d sources (first quality=%q url=%q)", len(sources), sources[0].Quality, sources[0].URL)

	resolved := model.ResolvedMedia{
		SeriesTitle:  "Inception",
		EpisodeTitle: "Inception",
		MediaType:    provider.MediaTypeMovie,
		TMDBID:       27205,
		Playback:     sources,
	}

	_ = os.Remove(player.DefaultMPVSocketPath())
	t.Cleanup(func() {
		quitMPV(t)
		time.Sleep(500 * time.Millisecond)
	})

	players := player.NewRegistry("mpv", player.SkipClients{}, player.SkipSettings{Provider: "off"})
	type playOutcome struct {
		result player.PlaybackResult
		err    error
	}
	done := make(chan playOutcome, 1)
	go func() {
		result, err := players.PlayWithSources(resolved.Playback, resolved, "mpv")
		done <- playOutcome{result: result, err: err}
	}()

	extDuration := pollMVPDuration(t, 30*time.Second)
	if extDuration > 0 {
		t.Logf("cinecat playback confirmed via mpv IPC duration=%.1fs", extDuration)
	} else {
		select {
		case out := <-done:
			if out.err != nil {
				t.Fatalf("player exited with error: %v", out.err)
			}
			if out.result.DurationSecs <= 0 && out.result.FinalPositionSecs <= 0 {
				t.Fatal("player exited without playing")
			}
		default:
			t.Fatalf("no IPC evidence of playback after 30s")
		}
	}

	quitMPV(t)
	select {
	case out := <-done:
		t.Logf("player returned after quit err=%v pos=%.1fs", out.err, out.result.FinalPositionSecs)
	case <-time.After(10 * time.Second):
		t.Log("player did not return within 10s of quit")
	}
}

// pollMVPDuration connects to the default mpv IPC socket until it
// reports a media duration greater than zero, or the deadline expires.
func pollMVPDuration(t *testing.T, within time.Duration) float64 {
	t.Helper()
	return pollMVPDurationOnSocket(t, player.DefaultMPVSocketPath(), within, true, nil)
}

// pollMVPDurationOnSocket polls one mpv IPC socket for a duration value.
// Quiet mode skips per-attempt logs for parallel audits. The abort
// channel, when non-nil, is closed when mpv's process exits: duration
// evidence never arrives after that, so polling stops at once instead
// of burning the whole timeout on a dead stream.
func pollMVPDurationOnSocket(t *testing.T, sock string, within time.Duration, verbose bool, abort <-chan struct{}) float64 {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		select {
		case <-abort:
			return finalDurationAttempt(sock)
		default:
		}
		c := player.NewIPCClient(sock)
		err := c.Connect(time.Second)
		if err != nil {
			if verbose {
				t.Logf("poll: connect failed sock=%s err=%v", sock, err)
			}
		} else {
			v, perr := c.GetProperty("duration")
			_ = c.Close()
			if perr == nil {
				if d, ok := v.(float64); ok && d > 0 {
					return d
				}
				if verbose {
					t.Logf("poll: connected, duration=%v (%T); still polling", v, v)
				}
			} else if verbose {
				t.Logf("poll: connected but duration err=%v", perr)
			}
		}
		time.Sleep(250 * time.Millisecond)
	}
	return 0
}

// quitMPV sends {"command":["quit"]} over the IPC socket via a raw dial.
// It does not use player internals: the quit command needs no reply, and the
// shared IPC client only supports request/response properties.
func quitMPV(t *testing.T) {
	t.Helper()
	quitMPVSocket(t, player.DefaultMPVSocketPath(), true)
}

// quitMPVSocket sends {"command":["quit"]} over one mpv IPC socket via a
// raw dial. It does not use player internals: the quit command needs no
// reply, and the shared IPC client only supports request/response
// properties. Quiet mode skips the log when nothing is listening.
func quitMPVSocket(t *testing.T, sock string, verbose bool) {
	t.Helper()
	conn, err := net.DialTimeout("unix", sock, 3*time.Second)
	if err != nil {
		if verbose {
			t.Logf("could not connect mpv IPC for quit: %v", err)
		}
		return
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
	_, _ = conn.Write([]byte(`{"command":["quit"]}` + "\n"))
}

// finalDurationAttempt makes one last duration read after mpv's process
// exits, covering the race where the stream loaded just as the process
// went away. It never logs: callers in this path are already failing.
func finalDurationAttempt(sock string) float64 {
	c := player.NewIPCClient(sock)
	if err := c.Connect(time.Second); err != nil {
		return 0
	}
	defer c.Close()
	v, err := c.GetProperty("duration")
	if err != nil {
		return 0
	}
	if d, ok := v.(float64); ok && d > 0 {
		return d
	}
	return 0
}
