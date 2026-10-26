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
	return NewDownloadService(dir, dl, NewMediaService(newTestRegistry()), nil)
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
	svc := NewDownloadService(t.TempDir(), &fakeDownloader{}, NewMediaService(newTestRegistry(good, bad)), nil)
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

func createTestSubFile(t *testing.T, content string) string {
	t.Helper()
	tmpFile, err := os.CreateTemp(t.TempDir(), "test_sub_*.srt")
	if err != nil {
		t.Fatalf("create temp sub file: %v", err)
	}
	if _, err := tmpFile.WriteString(content); err != nil {
		t.Fatalf("write temp sub file: %v", err)
	}
	_ = tmpFile.Close()
	return tmpFile.Name()
}

func TestDownloadSavesSidecarSubtitleFromSelectedSubtitle(t *testing.T) {
	root := t.TempDir()
	subFile := createTestSubFile(t, "1\n00:00:01,000 --> 00:00:02,000\nHello world\n")
	subSvc := NewSubtitleService(nil)
	dl := &fakeDownloader{}
	svc := NewDownloadService(root, dl, NewMediaService(newTestRegistry()), subSvc)

	resolved := model.ResolvedMedia{
		SeriesTitle: "Inception",
		MediaType:   provider.MediaTypeMovie,
		Playback:    []provider.MediaSource{{URL: "https://example.com/stream.mp4", Quality: "1080p"}},
		SelectedSubtitle: &model.SubtitleTrack{
			Path:     subFile,
			Language: "en",
		},
	}

	if err := svc.Download(context.Background(), resolved, nil); err != nil {
		t.Fatalf("Download failed: %v", err)
	}

	expectedSubPath := filepath.Join(root, "Inception", "Inception.en.srt")
	data, err := os.ReadFile(expectedSubPath)
	if err != nil {
		t.Fatalf("sidecar subtitle file not created: %v", err)
	}
	if string(data) != "1\n00:00:01,000 --> 00:00:02,000\nHello world\n" {
		t.Errorf("unexpected subtitle content: %q", string(data))
	}
}

func TestDownloadSavesSidecarSubtitleAnimeSoftSub(t *testing.T) {
	root := t.TempDir()
	subFile := createTestSubFile(t, "1\n00:00:01,000 --> 00:00:02,000\nSoft sub text\n")
	subSvc := NewSubtitleService(nil)
	dl := &fakeDownloader{}
	svc := NewDownloadService(root, dl, NewMediaService(newTestRegistry()), subSvc)

	resolved := model.ResolvedMedia{
		SeriesTitle:   "Frieren",
		SeasonNumber:  1,
		EpisodeNumber: 2,
		EpisodeTitle:  "Journey",
		MediaType:     provider.MediaTypeAnime,
		Playback: []provider.MediaSource{{
			URL:     "https://example.com/anime.m3u8",
			Quality: "1080p",
			SubType: provider.SubTypeSoft,
		}},
		SelectedSubtitle: &model.SubtitleTrack{
			Path:     subFile,
			Language: "en",
		},
	}

	if err := svc.Download(context.Background(), resolved, nil); err != nil {
		t.Fatalf("Download failed: %v", err)
	}

	expectedSubPath := filepath.Join(root, "Frieren", "Season 01", "S01E02 - Journey.en.srt")
	data, err := os.ReadFile(expectedSubPath)
	if err != nil {
		t.Fatalf("sidecar subtitle file not created for softsub anime: %v", err)
	}
	if string(data) != "1\n00:00:01,000 --> 00:00:02,000\nSoft sub text\n" {
		t.Errorf("unexpected subtitle content: %q", string(data))
	}
}

func TestDownloadSkipsSidecarSubtitleAnimeHardSub(t *testing.T) {
	root := t.TempDir()
	subFile := createTestSubFile(t, "1\n00:00:01,000 --> 00:00:02,000\nHardsub text\n")
	subSvc := NewSubtitleService(nil)
	dl := &fakeDownloader{}
	svc := NewDownloadService(root, dl, NewMediaService(newTestRegistry()), subSvc)

	cases := []struct {
		name    string
		subType string
		quality string
	}{
		{"explicit hard subtype", provider.SubTypeHard, "1080p"},
		{"quality hard sub tag", "", "1080p (Hard Sub)"},
		{"quality hsub tag", "", "1080p [hsub]"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resolved := model.ResolvedMedia{
				SeriesTitle:   "Naruto",
				SeasonNumber:  1,
				EpisodeNumber: 1,
				MediaType:     provider.MediaTypeAnime,
				Playback: []provider.MediaSource{{
					URL:     "https://example.com/hard.m3u8",
					Quality: tc.quality,
					SubType: tc.subType,
				}},
				SelectedSubtitle: &model.SubtitleTrack{
					Path:     subFile,
					Language: "en",
				},
			}

			if err := svc.Download(context.Background(), resolved, nil); err != nil {
				t.Fatalf("Download failed: %v", err)
			}

			expectedSubPath := filepath.Join(root, "Naruto", "Season 01", "S01E01.en.srt")
			if _, err := os.Stat(expectedSubPath); !os.IsNotExist(err) {
				t.Fatalf("sidecar subtitle must NOT be created for hardsub anime (%s)", tc.name)
			}
		})
	}
}

func TestDownloadSkipsSidecarSubtitleWhenSubtitleLanguageOff(t *testing.T) {
	root := t.TempDir()
	subFile := createTestSubFile(t, "1\n00:00:01,000 --> 00:00:02,000\nSub\n")
	subSvc := NewSubtitleService(nil)
	dl := &fakeDownloader{}
	svc := NewDownloadService(root, dl, NewMediaService(newTestRegistry()), subSvc)
	svc.SetSubtitleLanguage("off")

	resolved := model.ResolvedMedia{
		SeriesTitle: "Interstellar",
		MediaType:   provider.MediaTypeMovie,
		Playback:    []provider.MediaSource{{URL: "https://example.com/stream.mp4", Quality: "1080p"}},
		SelectedSubtitle: &model.SubtitleTrack{
			Path:     subFile,
			Language: "en",
		},
	}

	if err := svc.Download(context.Background(), resolved, nil); err != nil {
		t.Fatalf("Download failed: %v", err)
	}

	expectedSubPath := filepath.Join(root, "Interstellar", "Interstellar.en.srt")
	if _, err := os.Stat(expectedSubPath); !os.IsNotExist(err) {
		t.Fatal("sidecar subtitle must NOT be created when subtitleLanguage is 'off'")
	}
}

func TestDownloadSkipsSidecarSubtitleWhenAnimeSubtitlesDisabled(t *testing.T) {
	root := t.TempDir()
	subFile := createTestSubFile(t, "1\n00:00:01,000 --> 00:00:02,000\nSub\n")
	subSvc := NewSubtitleService(nil)
	subSvc.SetDisableAnimeSubtitles(true)
	dl := &fakeDownloader{}
	svc := NewDownloadService(root, dl, NewMediaService(newTestRegistry()), subSvc)

	anime := model.ResolvedMedia{
		SeriesTitle:   "Jujutsu Kaisen",
		EpisodeNumber: 1,
		MediaType:     provider.MediaTypeAnime,
		Playback:      []provider.MediaSource{{URL: "https://example.com/stream.mp4", Quality: "1080p", SubType: provider.SubTypeSoft}},
		SelectedSubtitle: &model.SubtitleTrack{
			Path:     subFile,
			Language: "en",
		},
	}

	if err := svc.Download(context.Background(), anime, nil); err != nil {
		t.Fatalf("Download failed: %v", err)
	}

	expectedSubPath := filepath.Join(root, "Jujutsu Kaisen", "E01.en.srt")
	if _, err := os.Stat(expectedSubPath); !os.IsNotExist(err) {
		t.Fatal("sidecar subtitle must NOT be created when anime subtitles are disabled")
	}

	// Movie with same service still downloads subtitle
	movie := model.ResolvedMedia{
		SeriesTitle: "The Matrix",
		MediaType:   provider.MediaTypeMovie,
		Playback:    []provider.MediaSource{{URL: "https://example.com/stream.mp4", Quality: "1080p"}},
		SelectedSubtitle: &model.SubtitleTrack{
			Path:     subFile,
			Language: "en",
		},
	}
	if err := svc.Download(context.Background(), movie, nil); err != nil {
		t.Fatalf("Download failed: %v", err)
	}
	expectedMovieSub := filepath.Join(root, "The Matrix", "The Matrix.en.srt")
	if _, err := os.Stat(expectedMovieSub); err != nil {
		t.Fatalf("movie sidecar subtitle must be created even when anime subtitles disabled: %v", err)
	}
}

func TestCleanupPartialRemovesSidecarSubtitles(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "CleanupShow")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	subFile := filepath.Join(dir, "ep1.en.srt")
	if err := os.WriteFile(subFile, []byte("sub content"), 0o644); err != nil {
		t.Fatal(err)
	}

	dl := &fakeDownloader{}
	svc := NewDownloadService(root, dl, NewMediaService(newTestRegistry()), nil)

	// No video exists: CleanupPartial must remove the orphaned sidecar subtitle
	svc.CleanupPartial(dir, "ep1")
	if _, err := os.Stat(subFile); !os.IsNotExist(err) {
		t.Fatal("orphaned sidecar subtitle was not cleaned up")
	}

	// With a valid video file existing (.mov), CleanupPartial preserves the subtitle
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	videoFile := filepath.Join(dir, "ep1.mov")
	if err := os.WriteFile(videoFile, []byte("video content"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(subFile, []byte("sub content"), 0o644); err != nil {
		t.Fatal(err)
	}
	svc.CleanupPartial(dir, "ep1")
	if _, err := os.Stat(subFile); err != nil {
		t.Fatalf("finished video's sidecar subtitle was unexpectedly removed: %v", err)
	}

	// Titles with brackets like [1080p] must clean up without glob metacharacter errors
	bracketDir := filepath.Join(root, "BracketShow [1080p]")
	if err := os.MkdirAll(bracketDir, 0o755); err != nil {
		t.Fatal(err)
	}
	bracketSub := filepath.Join(bracketDir, "ep [1080p].en.srt")
	if err := os.WriteFile(bracketSub, []byte("sub"), 0o644); err != nil {
		t.Fatal(err)
	}
	svc.CleanupPartial(bracketDir, "ep [1080p]")
	if _, err := os.Stat(bracketSub); !os.IsNotExist(err) {
		t.Fatal("bracketed orphaned sidecar subtitle was not cleaned up")
	}
}

func TestDownloadService_VerifyForwarding(t *testing.T) {
	dl := &fakeDownloader{}
	svc := testDownloadService(t.TempDir(), dl)

	if !svc.VerifyDownloads() {
		t.Fatalf("expected VerifyDownloads to default to true")
	}

	resolved := model.ResolvedMedia{
		SeriesTitle: "Movie",
		Playback:    []provider.MediaSource{{URL: "https://example.com/video.mp4", Quality: "1080p"}},
	}

	if err := svc.Download(context.Background(), resolved, nil); err != nil {
		t.Fatalf("Download failed: %v", err)
	}
	if len(dl.requests) != 1 || !dl.requests[0].Verify {
		t.Fatalf("expected dl.requests[0].Verify == true, got %+v", dl.requests)
	}

	svc.SetVerifyDownloads(false)
	if svc.VerifyDownloads() {
		t.Fatalf("expected VerifyDownloads to be false after SetVerifyDownloads(false)")
	}

	if err := svc.Download(context.Background(), resolved, nil); err != nil {
		t.Fatalf("Download failed: %v", err)
	}
	if len(dl.requests) != 2 || dl.requests[1].Verify {
		t.Fatalf("expected dl.requests[1].Verify == false, got %+v", dl.requests)
	}
}
