//go:build live

package live

import (
	"os"
	"testing"
	"time"

	"kari/internal/model"
	"kari/internal/player"
	"kari/internal/provider"
)

// TestLiveMPVPlayback drives the production player path for every live
// provider: resolve a verified live source, build the same ResolvedMedia the
// TUI builds, and run MPVPlayer through the registry. Progress is observed
// through the registry's production observer, exactly like the TUI's Rich
// Presence feed.
func TestLiveMPVPlayback(t *testing.T) {
	reg := newRegistry(t)

	for _, p := range reg.ProvidersForMode(provider.ModeLive) {
		p := p
		t.Run(p.Name(), func(t *testing.T) {
			episodeSource, ok := p.(provider.EpisodeSource)
			if !ok {
				t.Skip("provider does not support playback")
			}
			verifier, _ := p.(provider.LiveStreamVerifier)
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
					if verifier != nil {
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
			playLiveSource(t, chosen, title)
		})
	}
}

// playLiveSource runs the production MPV player and waits for a progress
// sample with real position/duration evidence before quitting.
func playLiveSource(t *testing.T, src provider.MediaSource, title string) {
	t.Helper()
	resolved := model.ResolvedMedia{
		SeriesTitle: title,
		MediaType:   provider.MediaTypeLive,
		ContentMode: provider.ModeLive,
		Playback:    []provider.MediaSource{src},
	}
	_ = os.Remove(player.DefaultMPVSocketPath())
	t.Cleanup(func() {
		quitMPV(t)
		time.Sleep(500 * time.Millisecond)
	})

	players := player.NewRegistry("mpv", player.SkipClients{}, player.SkipSettings{Provider: "off"})
	progress := make(chan player.PlaybackProgress, 8)
	players.SetProgressObserver(func(p player.PlaybackProgress) {
		select {
		case progress <- p:
		default:
		}
	})

	done := make(chan error, 1)
	go func() {
		_, err := players.PlayWithSources(resolved.Playback, resolved, "mpv")
		done <- err
	}()

	deadline := time.After(40 * time.Second)
	for {
		select {
		case p := <-progress:
			if p.PositionSecs > 1 || p.DurationSecs > 0 {
				t.Logf("production player confirmed playback pos=%.1fs dur=%.1fs", p.PositionSecs, p.DurationSecs)
				quitMPV(t)
				select {
				case err := <-done:
					t.Logf("player returned after quit err=%v", err)
				case <-time.After(15 * time.Second):
					t.Log("player goroutine did not return within 15s of quit")
				}
				return
			}
		case err := <-done:
			t.Fatalf("player returned without playback: %v", err)
		case <-deadline:
			quitMPV(t)
			t.Fatal("no playback progress within 40s")
		}
	}
}
