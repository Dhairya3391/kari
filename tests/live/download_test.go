//go:build live

package live

import (
	"context"
	"os"
	"os/exec"
	"sync"
	"testing"
	"time"

	"kari/internal/downloader"
	"kari/internal/provider"
	"kari/internal/service"
)

// TestDownloadFetchesRealMedia resolves a title against live providers and
// downloads it with the production yt-dlp engine. Full episodes are large,
// so the test accepts either a completed download or progress that climbed
// past a small percentage within the budget — both prove the resolve→
// download pipeline works; zero progress fails.
func TestDownloadFetchesRealMedia(t *testing.T) {
	if _, err := exec.LookPath("yt-dlp"); err != nil {
		t.Skip("yt-dlp not installed")
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

	resolved, err := svc.Resolve(ctx, provider.ModeMovies, pick, provider.Episode{}, nil)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if len(resolved.Playback) == 0 {
		t.Fatal("resolve produced no playback sources")
	}
	sources := service.FilterPlaybackSources(resolved.Playback, 3 /* lowest quality */, nil)
	if len(sources) == 0 {
		t.Fatal("quality filter removed all sources")
	}
	resolved.Playback = sources
	t.Logf("downloading via %q (%s)", resolved.Playback[0].Quality, resolved.DisplayTitle())

	outDir := t.TempDir()
	var (
		mu         sync.Mutex
		maxPercent float64
	)
	dl := downloader.NewYTDLPDownloader()

	// A full episode is hundreds of MB; a bounded window proves the
	// pipeline while keeping CI-time sane. Cancelled context makes the
	// engine clean up its own partial files.
	runCtx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	err = dl.Download(runCtx, downloader.DownloadRequest{
		Title:     resolved.DisplayTitle(),
		OutputDir: outDir,
		Sources:   resolved.Playback,
		Progress: func(p downloader.DownloadProgress) {
			mu.Lock()
			if p.Percent > maxPercent {
				maxPercent = p.Percent
			}
			mu.Unlock()
		},
	})

	// Success means the resolve→download pipeline demonstrably moved real
	// bytes: either the file completed, or progress climbed while we gave
	// it a bounded window. (On budget expiry the engine deliberately
	// removes its .part artifacts, so post-hoc file size proves nothing.)
	completed := err == nil
	if !completed && runCtx.Err() == nil && ctx.Err() == nil {
		t.Fatalf("download failed outright: %v", err)
	}
	partial := findLargestFile(t, outDir)
	mu.Lock()
	recordedMax := maxPercent
	mu.Unlock()
	t.Logf("outcome: completed=%v maxProgress=%.1f%% artifactBytes=%d", completed, recordedMax*100, partial)

	const minMeaningfulProgress = 5.0 // percent
	if !completed && maxPercent*100 < minMeaningfulProgress {
		t.Fatalf("no meaningful download progress within budget: %.1f%%", maxPercent*100)
	}
}

// findLargestFile returns the size of the largest regular file under dir
// (yt-dlp writes .part files while streaming, which is exactly what we
// want to measure).
func findLargestFile(t *testing.T, dir string) int64 {
	t.Helper()
	var largest int64
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read output dir: %v", err)
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if info.Size() > largest {
			largest = info.Size()
		}
	}
	return largest
}
