//go:build live

package live

import (
	"os"
	"os/exec"
	"testing"
	"time"

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

	resolved, err := svc.Resolve(ctx, provider.ModeMovies, pick, provider.Episode{}, nil)
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
	t.Logf("resolved %d sources; attempting playback of %q (%s)", len(lowest), lowest[0].Quality, lowest[0].URL)

	// Clear any stale socket from a previous crashed run.
	_ = os.Remove(player.DefaultMPVSocketPath())

	// Whatever happens below — assertion failure, timeout, panic — never
	// leave an orphaned mpv playing on this machine.
	t.Cleanup(func() {
		quitMPV(t)
		time.Sleep(500 * time.Millisecond)
	})

	players := player.NewRegistry("mpv", nil, nil, player.SkipSettings{Provider: "off"})
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
		// Any return is fine now — we only needed proof the stream loaded.
		t.Logf("player returned after quit err=%v pos=%.1fs", out.err, out.result.FinalPositionSecs)
	case <-time.After(15 * time.Second):
		t.Log("player goroutine did not return within 15s of quit; continuing")
	}
}

// pollMVPDuration connects to the mpv IPC socket until it reports a media
// duration greater than zero, or the deadline expires. Each successful
// observation is logged so failures carry diagnostics.
func pollMVPDuration(t *testing.T, within time.Duration) float64 {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		sock := player.DefaultMPVSocketPath()
		c := player.NewIPCClient(sock)
		err := c.Connect(time.Second)
		if err != nil {
			t.Logf("poll: connect failed sock=%s err=%v", sock, err)
		} else {
			v, perr := c.GetProperty("duration")
			_ = c.Close()
			if perr == nil {
				if d, ok := v.(float64); ok && d > 0 {
					return d
				}
				t.Logf("poll: connected, duration=%v (%T); still polling", v, v)
			} else {
				t.Logf("poll: connected but duration err=%v", perr)
			}
		}
		time.Sleep(250 * time.Millisecond)
	}
	return 0
}

// quitMPV sends {"command":["quit"]} over the IPC socket.
func quitMPV(t *testing.T) {
	t.Helper()
	ipc := player.NewIPCClient(player.DefaultMPVSocketPath())
	if err := ipc.Connect(3 * time.Second); err != nil {
		t.Logf("could not connect mpv IPC for quit: %v", err)
		return
	}
	defer ipc.Close()
	_ = ipc.SendCommand("quit")
}
