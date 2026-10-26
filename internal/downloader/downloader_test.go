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
