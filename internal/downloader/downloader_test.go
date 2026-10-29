package downloader

// Pins header derivation for downloads: fields come from
// media.Source.Headers via toMediaSource, emitted in deterministic order.

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"kari/internal/provider"
)

func TestSourceHeaders(t *testing.T) {
	got := sourceHeaders(provider.MediaSource{
		URL: "https://cdn.example.com/x.m3u8", UserAgent: "UA",
		Referer: "https://megaplay.buzz/watch/1", CookieHeader: "a=b",
	})
	want := []string{
		"User-Agent: UA",
		"Referer: https://megaplay.buzz/watch/1",
		"Origin: https://megaplay.buzz",
		"Cookie: a=b",
	}
	if len(got) != len(want) {
		t.Fatalf("sourceHeaders = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("sourceHeaders[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestSourceHeadersSuppressOrigin(t *testing.T) {
	got := sourceHeaders(provider.MediaSource{Referer: "https://megaplay.buzz/watch/1", SuppressOrigin: true})
	for _, h := range got {
		if len(h) >= 6 && h[:6] == "Origin" {
			t.Errorf("SuppressOrigin must drop Origin, got %v", got)
		}
	}
}

func TestSourceHeadersEmpty(t *testing.T) {
	if got := sourceHeaders(provider.MediaSource{URL: "https://cdn.example.com/x.m3u8"}); len(got) != 0 {
		t.Errorf("empty source must yield no headers, got %v", got)
	}
}

func TestFindOutputPathAndSize(t *testing.T) {
	dir := t.TempDir()
	dl := NewYTDLPDownloader()

	if path := dl.findOutputPath(dir, "movie"); path != "" {
		t.Errorf("expected empty path, got %q", path)
	}

	target := filepath.Join(dir, "movie.mp4")
	data := make([]byte, 2*1024*1024) // 2MB
	if err := os.WriteFile(target, data, 0o644); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	if path := dl.findOutputPath(dir, "movie"); path != target {
		t.Errorf("findOutputPath = %q, want %q", path, target)
	}
	if size := dl.findOutputSize(dir, "movie"); size == "" {
		t.Errorf("findOutputSize expected non-empty size string")
	}

	// If .aria2 control file exists, file is considered incomplete
	ariaPath := target + ".aria2"
	if err := os.WriteFile(ariaPath, []byte("aria"), 0o644); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}
	if path := dl.findOutputPath(dir, "movie"); path != "" {
		t.Errorf("expected empty path when .aria2 exists, got %q", path)
	}
	_ = os.Remove(ariaPath)

	dl.removeDownloadedFile(dir, "movie")
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Errorf("expected file to be removed by removeDownloadedFile")
	}
}

func TestVerifyFile_NotFound(t *testing.T) {
	dir := t.TempDir()
	dl := NewYTDLPDownloader()
	err := dl.verifyFile(context.Background(), dir, "missing")
	if err == nil || !strings.Contains(err.Error(), "output file not found") {
		t.Fatalf("expected output file not found error, got %v", err)
	}
}

func TestVerifyFile_NoFFmpeg(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "video.mp4")
	if err := os.WriteFile(target, []byte("dummy media data"), 0o644); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	t.Setenv("PATH", "")
	dl := NewYTDLPDownloader()
	if err := dl.verifyFile(context.Background(), dir, "video"); err != nil {
		t.Fatalf("expected nil when ffmpeg is missing, got %v", err)
	}
}

func TestVerifyFile_ValidAndCorrupt(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not installed; skipping ffmpeg verification test")
	}

	dir := t.TempDir()
	target := filepath.Join(dir, "sample.mp4")

	// Generate a 1-second valid test video with libx264
	cmd := exec.Command("ffmpeg", "-y", "-v", "error", "-f", "lavfi", "-i", "testsrc=duration=1:size=320x240:rate=10", "-c:v", "libx264", target)
	if err := cmd.Run(); err != nil {
		t.Fatalf("failed to generate test video: %v", err)
	}

	dl := NewYTDLPDownloader()
	if err := dl.verifyFile(context.Background(), dir, "sample"); err != nil {
		t.Fatalf("expected valid video to pass verification, got %v", err)
	}

	// Corrupt bytes in the middle of the video stream
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}
	mid := len(data) / 2
	for i := 0; i < 64 && mid+i < len(data); i++ {
		data[mid+i] = 0xFF
	}
	if err := os.WriteFile(target, data, 0o644); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	err = dl.verifyFile(context.Background(), dir, "sample")
	if err == nil || !strings.Contains(err.Error(), "video integrity check failed") {
		t.Fatalf("expected integrity check failure on corrupt video, got %v", err)
	}

	dl.removeDownloadedFile(dir, "sample")
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Errorf("expected corrupt video to be removed by removeDownloadedFile")
	}
}

func TestFindOutputPath_CaseInsensitive(t *testing.T) {
	dir := t.TempDir()
	dl := NewYTDLPDownloader()

	upperPath := filepath.Join(dir, "uppercase.MP4")
	if err := os.WriteFile(upperPath, []byte("fake video content"), 0o644); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	found := dl.findOutputPath(dir, "uppercase")
	if found != upperPath {
		t.Fatalf("expected to find %s, got %s", upperPath, found)
	}
}

func TestVerifyFile_LongVideoFast(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not installed; skipping ffmpeg verification test")
	}

	dir := t.TempDir()
	target := filepath.Join(dir, "long_sample.mp4")

	// Generate a 10-second test video
	cmd := exec.Command("ffmpeg", "-y", "-v", "error", "-f", "lavfi", "-i", "testsrc=duration=10:size=320x240:rate=10", "-c:v", "libx264", target)
	if err := cmd.Run(); err != nil {
		t.Fatalf("failed to generate test video: %v", err)
	}

	dl := NewYTDLPDownloader()
	start := time.Now()
	if err := dl.verifyFile(context.Background(), dir, "long_sample"); err != nil {
		t.Fatalf("expected valid video to pass verification, got %v", err)
	}
	elapsed := time.Since(start)
	if elapsed > 3*time.Second {
		t.Fatalf("verification took too long (%v), expected < 3s for sampled checks", elapsed)
	}
}

func TestVerifyFile_TruncatedTail(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not installed; skipping ffmpeg verification test")
	}

	dir := t.TempDir()
	target := filepath.Join(dir, "truncated.mp4")

	// Generate a 6-second valid test video
	cmd := exec.Command("ffmpeg", "-y", "-v", "error", "-f", "lavfi", "-i", "testsrc=duration=6:size=320x240:rate=10", "-c:v", "libx264", target)
	if err := cmd.Run(); err != nil {
		t.Fatalf("failed to generate test video: %v", err)
	}

	// Truncate the file by 30% to simulate an incomplete/interrupted download
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}
	truncatedSize := int(float64(len(data)) * 0.7)
	if err := os.WriteFile(target, data[:truncatedSize], 0o644); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	dl := NewYTDLPDownloader()
	err = dl.verifyFile(context.Background(), dir, "truncated")
	if err == nil || !strings.Contains(err.Error(), "video integrity check failed") {
		t.Fatalf("expected integrity check failure on truncated video, got %v", err)
	}
}

func TestVerifyFile_TruncatedMKV(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not installed; skipping ffmpeg verification test")
	}

	dir := t.TempDir()
	target := filepath.Join(dir, "truncated.mkv")

	// Generate an 8-second valid test MKV video
	cmd := exec.Command("ffmpeg", "-y", "-v", "error", "-f", "lavfi", "-i", "testsrc=duration=8:size=320x240:rate=10", "-c:v", "libx264", target)
	if err := cmd.Run(); err != nil {
		t.Fatalf("failed to generate test MKV video: %v", err)
	}

	// Truncate the file by 30% to simulate an incomplete/interrupted download
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}
	truncatedSize := int(float64(len(data)) * 0.7)
	if err := os.WriteFile(target, data[:truncatedSize], 0o644); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	dl := NewYTDLPDownloader()
	err = dl.verifyFile(context.Background(), dir, "truncated")
	if err == nil || !strings.Contains(err.Error(), "video integrity check failed") {
		t.Fatalf("expected integrity check failure on truncated MKV, got %v", err)
	}
}

func TestCleanupPartial_WithTempDirs(t *testing.T) {
	outDir := t.TempDir()
	tempDir := filepath.Join(outDir, ".tmp")
	if err := os.MkdirAll(tempDir, 0o755); err != nil {
		t.Fatal(err)
	}

	part1 := filepath.Join(outDir, "ep1.mp4.part")
	part2 := filepath.Join(tempDir, "ep1.mp4.part-Frag1")
	ytdl := filepath.Join(tempDir, "ep1.mp4.ytdl")
	aria := filepath.Join(tempDir, "ep1.mp4.aria2")
	ariaMedia := filepath.Join(tempDir, "ep1.mp4")
	keep := filepath.Join(outDir, "other.mp4")

	for _, p := range []string{part1, part2, ytdl, aria, ariaMedia, keep} {
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	dl := NewYTDLPDownloader()
	dl.CleanupPartial(outDir, "ep1", tempDir)

	for _, p := range []string{part1, part2, ytdl, aria, ariaMedia} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("file %s should have been cleaned up", p)
		}
	}
	if _, err := os.Stat(keep); err != nil {
		t.Errorf("file %s should have been preserved", keep)
	}
	if _, err := os.Stat(tempDir); !os.IsNotExist(err) {
		t.Errorf("tempDir should have been pruned when empty")
	}
}
func TestCleanupPartial_PreservesOutputDirWithTmpInName(t *testing.T) {
	parent := t.TempDir()
	outDir := filepath.Join(parent, ".tmp_user_downloads")
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		t.Fatal(err)
	}
	partFile := filepath.Join(outDir, "ep1.mp4.part")
	if err := os.WriteFile(partFile, []byte("part data"), 0o644); err != nil {
		t.Fatal(err)
	}

	dl := NewYTDLPDownloader()
	dl.CleanupPartial(outDir, "ep1")

	if _, err := os.Stat(partFile); !os.IsNotExist(err) {
		t.Errorf("part file should be deleted")
	}
	if _, err := os.Stat(outDir); err != nil {
		t.Errorf("outDir should be preserved even if its path contains .tmp: %v", err)
	}
}

func TestRenameOutputFile_OverwritesExistingTarget(t *testing.T) {
	outDir := t.TempDir()
	tempDir := t.TempDir()

	desiredPath := filepath.Join(outDir, "episode.mp4")
	if err := os.WriteFile(desiredPath, []byte("old content"), 0o644); err != nil {
		t.Fatal(err)
	}

	actualPath := filepath.Join(tempDir, "temp_download.mp4")
	if err := os.WriteFile(actualPath, []byte("new content"), 0o644); err != nil {
		t.Fatal(err)
	}

	status := &aria2TellStatusResult{
		Files: []struct {
			Path string `json:"path"`
		}{{Path: actualPath}},
	}

	if err := renameOutputFile(status, outDir, "episode"); err != nil {
		t.Fatalf("renameOutputFile failed: %v", err)
	}

	content, err := os.ReadFile(desiredPath)
	if err != nil {
		t.Fatalf("ReadFile desiredPath failed: %v", err)
	}
	if string(content) != "new content" {
		t.Errorf("desiredPath content = %q, want 'new content'", string(content))
	}
	if _, err := os.Stat(actualPath); !os.IsNotExist(err) {
		t.Errorf("actualPath should no longer exist in tempDir")
	}
}

func TestRenameOutputFile_SameFileNoOp(t *testing.T) {
	outDir := t.TempDir()
	targetPath := filepath.Join(outDir, "episode.mp4")
	if err := os.WriteFile(targetPath, []byte("intact media"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Redundant path referencing the exact same file
	redundantPath := filepath.Join(outDir, ".", "episode.mp4")
	status := &aria2TellStatusResult{
		Files: []struct {
			Path string `json:"path"`
		}{{Path: redundantPath}},
	}

	if err := renameOutputFile(status, outDir, "episode"); err != nil {
		t.Fatalf("renameOutputFile failed: %v", err)
	}

	content, err := os.ReadFile(targetPath)
	if err != nil {
		t.Fatalf("targetPath was deleted: %v", err)
	}
	if string(content) != "intact media" {
		t.Errorf("content = %q, want 'intact media'", string(content))
	}
}

func TestMoveFileCrossFS(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "source.bin")
	dst := filepath.Join(dir, "dest.bin")

	if err := os.WriteFile(src, []byte("binary payload"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := moveFileCrossFS(src, dst); err != nil {
		t.Fatalf("moveFileCrossFS failed: %v", err)
	}

	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("failed to read dst: %v", err)
	}
	if string(got) != "binary payload" {
		t.Errorf("got %q, want 'binary payload'", string(got))
	}
	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Errorf("src should be removed after move")
	}
}

func TestSanitizeDownloadTitle(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"con", "download_con"},
		{"con.mp4", "download_con.mp4"},
		{"AUX", "download_AUX"},
		{"aux.mkv", "download_aux.mkv"},
		{"NUL", "download_NUL"},
		{"NUL.mp4", "download_NUL.mp4"},
		{"COM1", "download_COM1"},
		{"Lpt3", "download_Lpt3"},
		{"Show / Movie: Part 1?", "Show - Movie- Part 1-"},
		{"Clean Title", "Clean Title"},
		{"Tab\tNewline\nSpaces", "Tab Newline Spaces"},
		{"Contains\x7fDEL", "ContainsDEL"},
		{"", "download"},
		{"   ", "download"},
		{".", "download"},
		{"..", "download"},
	}
	for _, tc := range cases {
		if got := sanitizeDownloadTitle(tc.in); got != tc.want {
			t.Errorf("sanitizeDownloadTitle(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestYTDLPDownloader_MissingBinary(t *testing.T) {
	t.Setenv("PATH", "")
	dl := NewYTDLPDownloader()
	req := DownloadRequest{
		Sources:   []provider.MediaSource{{URL: "https://example.com/video.mp4"}},
		Title:     "video",
		OutputDir: t.TempDir(),
	}
	err := dl.Download(context.Background(), req)
	if err == nil || !strings.Contains(err.Error(), "yt-dlp: binary not found in PATH") {
		t.Fatalf("expected binary not found in PATH error, got %v", err)
	}
}
