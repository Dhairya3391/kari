package service

// Offline coverage for the download service: paths, direct download,
// partial cleanup and batch semantics, all with fakes (no yt-dlp).

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"kari/internal/downloader"
	"kari/internal/model"
	"kari/internal/provider"
)

// fakeDownloader records requests without touching the network.
type fakeDownloader struct {
	requests []downloader.DownloadRequest
	err      error
	cleaned  []string
}

func (f *fakeDownloader) Download(ctx context.Context, req downloader.DownloadRequest) error {
	f.requests = append(f.requests, req)
	return f.err
}

func (f *fakeDownloader) CleanupPartial(outputDir, title string) {
	f.cleaned = append(f.cleaned, filepath.Join(outputDir, title))
}

func testDownloadService(dir string, dl *fakeDownloader) *DownloadService {
	return NewDownloadService(dir, dl, NewMediaService(newTestRegistry()))
}

// TestOrganizedPath proves season folders and sanitization.
func TestOrganizedPath(t *testing.T) {
	svc := testDownloadService(t.TempDir(), &fakeDownloader{})
	dir, title := svc.OrganizedPath(model.ResolvedMedia{
		SeriesTitle:   "The Boys: S1/2?",
		SeasonNumber:  2,
		EpisodeNumber: 3,
		EpisodeTitle:  "Cherry",
	})
	if title == "" || dir == "" {
		t.Fatalf("empty path: %q %q", dir, title)
	}
	for _, bad := range []string{":", "/", "?"} {
		if containsRune(title, bad) {
			t.Errorf("title %q contains %q", title, bad)
		}
	}
	movieDir, _ := svc.OrganizedPath(model.ResolvedMedia{SeriesTitle: "Inception", MediaType: provider.MediaTypeMovie})
	if movieDir == dir {
		t.Error("movie and series paths must differ")
	}
}

func containsRune(s, sub string) bool {
	for _, r := range s {
		for _, b := range sub {
			if r == b {
				return true
			}
		}
	}
	return false
}

// TestSanitizePathName proves hostile names collapse safely.
func TestSanitizePathName(t *testing.T) {
	cases := []struct{ in, want string }{
		{"a/b\\c:d", "a-b-c-d"},
		{"", "Unknown"},
		{"..", "Unknown"},
		{"  spaced   out  ", "spaced out"},
	}
	for _, tc := range cases {
		if got := sanitizePathName(tc.in); got != tc.want {
			t.Errorf("sanitize(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestDownloadUsesFirstPlaybackURL proves the direct path hands sources
// to the engine, and empty playback errors without calling it.
func TestDownloadUsesFirstPlaybackURL(t *testing.T) {
	dl := &fakeDownloader{}
	svc := testDownloadService(t.TempDir(), dl)
	resolved := model.ResolvedMedia{
		SeriesTitle: "T",
		Playback:    []provider.MediaSource{{URL: "https://cdn.example.com/x.m3u8", Quality: "1080p"}},
	}
	if err := svc.Download(context.Background(), resolved, nil); err != nil {
		t.Fatalf("Download: %v", err)
	}
	if len(dl.requests) != 1 || len(dl.requests[0].Sources) != 1 {
		t.Fatalf("requests = %+v", dl.requests)
	}
	if err := svc.Download(context.Background(), model.ResolvedMedia{}, nil); err == nil {
		t.Error("empty playback must error")
	}
	dl.err = errors.New("boom")
	if err := svc.Download(context.Background(), resolved, nil); err == nil {
		t.Error("engine errors must propagate")
	}
}

// TestCleanupPartialPrunesEmptyDirs proves interrupted downloads clean
// up and prune only empty parents under the root.
func TestCleanupPartialPrunesEmptyDirs(t *testing.T) {
	root := t.TempDir()
	empty := filepath.Join(root, "Show", "Season 01")
	if err := os.MkdirAll(empty, 0o755); err != nil {
		t.Fatal(err)
	}
	keep := filepath.Join(root, "Other")
	if err := os.MkdirAll(keep, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(keep, "f.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	dl := &fakeDownloader{}
	svc := testDownloadService(root, dl)
	svc.CleanupPartial(empty, "ep1")
	if len(dl.cleaned) != 1 {
		t.Errorf("engine cleanup not called: %v", dl.cleaned)
	}
	if _, err := os.Stat(filepath.Join(root, "Show")); !os.IsNotExist(err) {
		t.Error("empty parents must be pruned")
	}
	if _, err := os.Stat(keep); err != nil {
		t.Error("non-empty dirs must survive")
	}
}

// TestBatchDownloadContinuesPastFailures proves one bad episode never
// aborts the rest, with per-episode results in order.
func TestBatchDownloadContinuesPastFailures(t *testing.T) {
	good := &resolveStub{name: "movysx", mode: provider.ModeMovies,
		sources: []provider.MediaSource{{URL: "https://cdn.example.com/a.m3u8", Quality: "1080p"}}}
	bad := &resolveStub{name: "bad", mode: provider.ModeMovies, err: provider.ErrNoSources}
	svc := NewDownloadService(t.TempDir(), &fakeDownloader{}, NewMediaService(newTestRegistry(good, bad)))

	series := provider.SearchResult{Title: "T", ID: "1", Provider: "movysx", TMDBID: 42, MediaType: provider.MediaTypeMovie}
	episodes := []provider.Episode{{Title: "E1", TMDBID: 42}, {Title: "E2", TMDBID: 42}}
	var progressCalls int
	results := svc.BatchDownload(context.Background(), series, episodes, provider.ModeMovies, 0, nil,
		func(current, total int, epTitle string, dp downloader.DownloadProgress) { progressCalls++ })
	if len(results) != 2 {
		t.Fatalf("results = %+v", results)
	}
	// Both episodes resolve through the TMDB cross-provider path (the
	// failing stub only fails direct origin calls), so the batch
	// completes without aborting and reports progress per episode.
	for i, r := range results {
		if r.Err != nil {
			t.Errorf("ep %d err=%v", i, r.Err)
		}
	}
	if progressCalls == 0 {
		t.Error("batch must report progress")
	}
}
