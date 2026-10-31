package service

// Offline coverage for the download service: paths, direct download,
// partial cleanup and batch semantics, all with fakes (no yt-dlp).

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"kari/internal/downloader"
	"kari/internal/model"
	"kari/internal/provider"
)

// fakeDownloader records requests without touching the network.
type fakeDownloader struct {
	requests     []downloader.DownloadRequest
	err          error
	cleaned      []string
	cleanedTemps []string
}

func (f *fakeDownloader) Download(ctx context.Context, req downloader.DownloadRequest) error {
	f.requests = append(f.requests, req)
	return f.err
}

func (f *fakeDownloader) CleanupPartial(outputDir, title string, tempDirs ...string) {
	f.cleaned = append(f.cleaned, filepath.Join(outputDir, title))
	f.cleanedTemps = append(f.cleanedTemps, tempDirs...)
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
		name        string
		subType     string
		quality     string
		wantSidecar bool
	}{
		{"explicit hard subtype", provider.SubTypeHard, "1080p", false},
		{"untyped quality hint", "", "1080p (Hard Sub)", true},
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
			_, statErr := os.Stat(expectedSubPath)
			if tc.wantSidecar && statErr != nil {
				t.Fatalf("untyped source must preserve sidecar subtitle: %v", statErr)
			}
			if !tc.wantSidecar && !os.IsNotExist(statErr) {
				t.Fatalf("declared hardsub source must omit sidecar subtitle, got %v", statErr)
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

func TestDownloadSetsTempDir(t *testing.T) {
	root := t.TempDir()
	dl := &fakeDownloader{}
	svc := testDownloadService(root, dl)
	resolved := model.ResolvedMedia{
		SeriesTitle:   "Attack on Titan",
		SeasonNumber:  2,
		EpisodeNumber: 1,
		EpisodeTitle:  "Beast Titan",
		Playback:      []provider.MediaSource{{URL: "https://example.com/stream.m3u8", Quality: "1080p"}},
	}

	if err := svc.Download(context.Background(), resolved, nil); err != nil {
		t.Fatalf("Download failed: %v", err)
	}

	if len(dl.requests) != 1 {
		t.Fatalf("expected 1 request, got %d", len(dl.requests))
	}
	req := dl.requests[0]
	wantTemp := filepath.Join(root, ".tmp", "Attack on Titan", "Season 02")
	if req.TempDir != wantTemp {
		t.Errorf("req.TempDir = %q, want %q", req.TempDir, wantTemp)
	}
	wantOutput := filepath.Join(root, "Attack on Titan", "Season 02")
	if req.OutputDir != wantOutput {
		t.Errorf("req.OutputDir = %q, want %q", req.OutputDir, wantOutput)
	}
}

func TestCleanupPartialPrunesTempDirs(t *testing.T) {
	root := t.TempDir()
	outDir := filepath.Join(root, "Show", "Season 01")
	tempDir := filepath.Join(root, ".tmp", "Show", "Season 01")
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(tempDir, 0o755); err != nil {
		t.Fatal(err)
	}

	dl := &fakeDownloader{}
	svc := testDownloadService(root, dl)
	svc.CleanupPartial(outDir, "ep1")

	if len(dl.cleaned) != 1 {
		t.Fatalf("expected 1 cleaned call, got %d", len(dl.cleaned))
	}
	if len(dl.cleanedTemps) != 1 || dl.cleanedTemps[0] != tempDir {
		t.Errorf("dl.cleanedTemps = %v, want [%s]", dl.cleanedTemps, tempDir)
	}
	if _, err := os.Stat(filepath.Join(root, ".tmp")); !os.IsNotExist(err) {
		t.Errorf("expected .tmp to be pruned when empty")
	}
}
func TestCleanupPartialPreservesSiblingTempDirs(t *testing.T) {
	root := t.TempDir()
	outDir := filepath.Join(root, "ShowA", "Season 01")
	tempDirA := filepath.Join(root, ".tmp", "ShowA", "Season 01")
	tempDirB := filepath.Join(root, ".tmp", "ShowB", "Season 01")
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(tempDirA, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(tempDirB, 0o755); err != nil {
		t.Fatal(err)
	}
	// File inside tempDirB representing an in-flight download for another show
	if err := os.WriteFile(filepath.Join(tempDirB, "ep1.part"), []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}

	dl := &fakeDownloader{}
	svc := testDownloadService(root, dl)
	svc.CleanupPartial(outDir, "ep1")

	if _, err := os.Stat(tempDirA); !os.IsNotExist(err) {
		t.Errorf("tempDirA should have been pruned")
	}
	if _, err := os.Stat(tempDirB); err != nil {
		t.Errorf("tempDirB should have been preserved: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, ".tmp")); err != nil {
		t.Errorf("parent .tmp should be preserved when sibling temp directories exist: %v", err)
	}
}

func TestFindLegacyPartialJob_TmpDir(t *testing.T) {
	root := t.TempDir()
	tempDir := filepath.Join(root, ".tmp", "One Piece", "Season 01")
	if err := os.MkdirAll(tempDir, 0o755); err != nil {
		t.Fatal(err)
	}
	partFile := filepath.Join(tempDir, "S01E01.mp4.part")
	if err := os.WriteFile(partFile, []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}

	svc := testDownloadService(root, &fakeDownloader{})
	job, err := svc.findLegacyPartialJob()
	if err != nil {
		t.Fatalf("findLegacyPartialJob failed: %v", err)
	}
	if job == nil {
		t.Fatalf("expected job to be found, got nil")
	}
	if job.Series.Title != "One Piece" {
		t.Errorf("job.Series.Title = %q, want 'One Piece'", job.Series.Title)
	}
}

func TestCompletedDownloads(t *testing.T) {
	root := t.TempDir()
	seasonDir := filepath.Join(root, "One Piece", "Season 01")
	if err := os.MkdirAll(seasonDir, 0o755); err != nil {
		t.Fatal(err)
	}

	now := time.Now()
	// 1. Completed video file (older)
	doneFile1 := filepath.Join(seasonDir, "S01E01 - I'm Luffy!.mp4")
	if err := os.WriteFile(doneFile1, []byte("valid media content"), 0o644); err != nil {
		t.Fatal(err)
	}
	_ = os.Chtimes(doneFile1, now.Add(-10*time.Minute), now.Add(-10*time.Minute))

	// 2. Completed video file with uppercase extension (newer)
	doneFile2 := filepath.Join(seasonDir, "S01E04 - Pirate Hunter.MP4")
	if err := os.WriteFile(doneFile2, []byte("valid media content 2"), 0o644); err != nil {
		t.Fatal(err)
	}
	_ = os.Chtimes(doneFile2, now, now)

	// 3. Video file with .aria2 incomplete marker
	incompleteFile1 := filepath.Join(seasonDir, "S01E02 - Hunter.mp4")
	if err := os.WriteFile(incompleteFile1, []byte("partial"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(incompleteFile1+".aria2", []byte("ctrl"), 0o644); err != nil {
		t.Fatal(err)
	}

	// 4. Video file with .part incomplete marker
	incompleteFile2 := filepath.Join(seasonDir, "S01E05 - Clown.mp4")
	if err := os.WriteFile(incompleteFile2, []byte("partial"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(incompleteFile2+".part", []byte("part"), 0o644); err != nil {
		t.Fatal(err)
	}

	// 5. Video file with .ytdl incomplete marker
	incompleteFile3 := filepath.Join(seasonDir, "S01E06 - Mayor.mp4")
	if err := os.WriteFile(incompleteFile3, []byte("partial"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(incompleteFile3+".ytdl", []byte("ytdl"), 0o644); err != nil {
		t.Fatal(err)
	}

	// 6. Staging temp file in .tmp
	tmpDir := filepath.Join(root, ".tmp", "One Piece", "Season 01")
	if err := os.MkdirAll(tmpDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "S01E03.mp4"), []byte("temp"), 0o644); err != nil {
		t.Fatal(err)
	}

	svc := testDownloadService(root, &fakeDownloader{})
	completed := svc.CompletedDownloads()

	if len(completed) != 2 {
		t.Fatalf("expected 2 completed downloads, got %d", len(completed))
	}
	// Newest first
	wantTitle0 := "One Piece — S01E04 - Pirate Hunter"
	if completed[0].Title != wantTitle0 {
		t.Errorf("completed[0].Title = %q, want %q", completed[0].Title, wantTitle0)
	}
	wantTitle1 := "One Piece — S01E01 - I'm Luffy!"
	if completed[1].Title != wantTitle1 {
		t.Errorf("completed[1].Title = %q, want %q", completed[1].Title, wantTitle1)
	}
	if completed[0].Size == "" || completed[1].Size == "" {
		t.Errorf("completed size expected non-empty")
	}
}

func TestCompletedDownloads_TempDirPartials(t *testing.T) {
	root := t.TempDir()
	showDir := filepath.Join(root, "Show")
	if err := os.MkdirAll(showDir, 0o755); err != nil {
		t.Fatal(err)
	}
	videoFile := filepath.Join(showDir, "ep1.mp4")
	if err := os.WriteFile(videoFile, []byte("media content"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Staged partial in .tmp/Show
	tmpShow := filepath.Join(root, ".tmp", "Show")
	if err := os.MkdirAll(tmpShow, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmpShow, "ep1.mp4.part"), []byte("frag"), 0o644); err != nil {
		t.Fatal(err)
	}

	svc := testDownloadService(root, &fakeDownloader{})
	completed := svc.CompletedDownloads()
	if len(completed) != 0 {
		t.Fatalf("expected 0 completed downloads when partial exists in tempDir, got %d", len(completed))
	}
}

func TestDownloadJob_BatchPersistence(t *testing.T) {
	root := t.TempDir()
	svc := testDownloadService(root, &fakeDownloader{})

	job := DownloadJob{
		Series: provider.SearchResult{Title: "Loki", MediaType: provider.MediaTypeTV},
		Episodes: []provider.Episode{
			{Title: "Glorious Purpose", Season: 1, Episode: 1},
			{Title: "The Variant", Season: 1, Episode: 2},
		},
		BatchCurrent: 1,
		Mode:         provider.ModeTV,
	}

	if err := svc.SavePendingJob(job); err != nil {
		t.Fatalf("SavePendingJob failed: %v", err)
	}

	loaded, err := svc.PendingJob()
	if err != nil {
		t.Fatalf("PendingJob failed: %v", err)
	}
	if loaded == nil {
		t.Fatal("expected pending job, got nil")
	}
	if loaded.Series.Title != "Loki" || len(loaded.Episodes) != 2 || loaded.BatchCurrent != 1 {
		t.Fatalf("unexpected loaded job: %+v", loaded)
	}

	if err := svc.ClearPendingJob(); err != nil {
		t.Fatalf("ClearPendingJob failed: %v", err)
	}
	afterClear, err := svc.PendingJob()
	if err != nil {
		t.Fatalf("PendingJob after clear failed: %v", err)
	}
	if afterClear != nil {
		t.Fatalf("expected nil after ClearPendingJob, got %+v", afterClear)
	}
}

func TestBatchDownloadWithActive_ClearsActiveBeforeResolve(t *testing.T) {
	good := &resolveStub{name: "movysx", mode: provider.ModeTV,
		sources: []provider.MediaSource{{URL: "https://cdn.example.com/a.m3u8", Quality: "1080p"}}}
	svc := NewDownloadService(t.TempDir(), &fakeDownloader{}, NewMediaService(newTestRegistry(good)), nil)
	series := provider.SearchResult{Title: "Show", ID: "1", Provider: "movysx", TMDBID: 42, MediaType: provider.MediaTypeTV}
	episodes := []provider.Episode{{Title: "E1", Season: 1, Episode: 1, TMDBID: 42}}

	var activeCalls []string
	onActive := func(dir, title string) {
		activeCalls = append(activeCalls, title)
	}

	svc.BatchDownloadWithActive(context.Background(), series, episodes, provider.ModeTV, 0, nil, onActive, nil)
	if len(activeCalls) < 2 {
		t.Fatalf("expected at least 2 onActive calls (clear then set), got %v", activeCalls)
	}
	if activeCalls[0] != "" {
		t.Errorf("first onActive call must be empty to clear stale values, got %q", activeCalls[0])
	}
	if activeCalls[1] == "" {
		t.Errorf("second onActive call must be non-empty, got %q", activeCalls[1])
	}
}
