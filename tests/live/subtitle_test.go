//go:build live

package live

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"kari/internal/config"
	"kari/internal/provider"
	"kari/internal/service"
)

func TestLiveSubtitleFetch(t *testing.T) {
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config load: %v", err)
	}

	reg := newRegistry(t)
	mediaSvc := service.NewMediaService(reg)
	subSvc := service.NewSubtitleService(cfg)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	results, _, _, err := mediaSvc.Search(ctx, provider.ModeMovies, "Fight Club")
	if err != nil {
		t.Fatalf("search failed: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("search returned 0 results")
	}

	pick := results[0]
	t.Logf("Selected title: %s (TMDB ID: %d, Provider: %s)", pick.Title, pick.TMDBID, pick.Provider)

	resolved, err := mediaSvc.Resolve(ctx, provider.ModeMovies, pick, provider.Episode{}, nil)
	if err != nil {
		t.Fatalf("resolve failed: %v", err)
	}

	t.Logf("Resolved %d playback sources and %d candidate subtitle tracks", len(resolved.Playback), len(resolved.Subtitles))
	for i, sub := range resolved.Subtitles {
		t.Logf("  Candidate %d: Lang=%q Label=%q Resolver=%q URL=%s", i, sub.Language, sub.Label, sub.Resolver, sub.URL)
	}

	// Test fetching subtitles with preferred language "en"
	for _, resolver := range []string{"rivestream", "vidking", "moviebox"} {
		start := time.Now()
		tracks, err := subSvc.Fetch(ctx, resolved, "en", resolver)
		duration := time.Since(start)

		if err != nil {
			t.Logf("[%s] Subtitle fetch returned: %v (took %v)", resolver, err, duration)
			continue
		}

		if len(tracks) == 0 {
			t.Logf("[%s] No tracks returned (took %v)", resolver, duration)
			continue
		}

		track := tracks[0]
		t.Logf("[%s] Successfully fetched subtitle in %v: Path=%s, Lang=%s, Label=%s", resolver, duration, track.Path, track.Language, track.Label)

		data, err := os.ReadFile(track.Path)
		if err != nil {
			t.Errorf("[%s] Failed to read downloaded subtitle file %s: %v", resolver, track.Path, err)
			continue
		}

		lines := strings.Split(string(data), "\n")
		t.Logf("[%s] Subtitle file verified: %d bytes, %d lines, first line: %q", resolver, len(data), len(lines), lines[0])

		// Verify basic SRT format
		if !strings.Contains(string(data), "-->") {
			t.Errorf("[%s] Downloaded subtitle does not contain timestamps (-->)", resolver)
		}
	}
}

func TestLiveProviderSubtitles(t *testing.T) {
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config load: %v", err)
	}

	reg := newRegistry(t)
	mediaSvc := service.NewMediaService(reg)
	subSvc := service.NewSubtitleService(cfg)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	results, _, _, err := mediaSvc.Search(ctx, provider.ModeAnime, "Naruto")
	if err != nil {
		t.Fatalf("search failed: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("search returned 0 results")
	}

	pick := results[0]
	t.Logf("Selected Anime title: %s (Provider: %s)", pick.Title, pick.Provider)

	// Get episodes first for anime
	eps, err := mediaSvc.FetchEpisodes(ctx, provider.ModeAnime, pick, "")
	if err != nil || len(eps) == 0 {
		t.Fatalf("fetch episodes: %v (count: %d)", err, len(eps))
	}

	resolved, err := mediaSvc.Resolve(ctx, provider.ModeAnime, pick, eps[0], nil)
	if err != nil {
		t.Fatalf("resolve failed: %v", err)
	}

	t.Logf("Resolved %d playback sources and %d candidate subtitle tracks", len(resolved.Playback), len(resolved.Subtitles))
	for i, sub := range resolved.Subtitles {
		t.Logf("  Provider Candidate %d: Lang=%q Label=%q Resolver=%q URL=%s", i, sub.Language, sub.Label, sub.Resolver, sub.URL)
	}

	if len(resolved.Subtitles) > 0 {
		start := time.Now()
		tracks, err := subSvc.Fetch(ctx, resolved, "en", resolved.Subtitles[0].Resolver)
		duration := time.Since(start)
		if err != nil {
			t.Fatalf("provider subtitle fetch failed: %v", err)
		}
		if len(tracks) == 0 {
			t.Fatal("expected at least 1 subtitle track")
		}

		track := tracks[0]
		t.Logf("Fetched direct provider subtitle in %v: Path=%s, Lang=%s, Label=%s", duration, track.Path, track.Language, track.Label)

		data, err := os.ReadFile(track.Path)
		if err != nil {
			t.Fatalf("read provider sub file: %v", err)
		}

		lines := strings.Split(string(data), "\n")
		t.Logf("Direct subtitle verified: %d bytes, %d lines, first line: %q", len(data), len(lines), lines[0])
		if !strings.Contains(string(data), "-->") {
			t.Errorf("expected SRT timestamps in provider subtitle")
		}
	}
}
