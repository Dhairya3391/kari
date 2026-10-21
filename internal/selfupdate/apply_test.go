package selfupdate

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestReleaseFindAssetURL(t *testing.T) {
	expectedAsset := fmt.Sprintf("kari-%s-%s", runtime.GOOS, runtime.GOARCH)
	if runtime.GOOS == "windows" {
		expectedAsset += ".exe"
	}

	rel := &Release{
		TagName: "v5.0.0",
		Assets: []struct {
			Name               string `json:"name"`
			BrowserDownloadURL string `json:"browser_download_url"`
		}{
			{Name: "kari-linux-amd64", BrowserDownloadURL: "https://example.com/kari-linux-amd64"},
			{Name: "kari-darwin-arm64", BrowserDownloadURL: "https://example.com/kari-darwin-arm64"},
			{Name: "kari-windows-amd64.exe", BrowserDownloadURL: "https://example.com/kari-windows-amd64.exe"},
			{Name: expectedAsset, BrowserDownloadURL: "https://example.com/" + expectedAsset},
		},
	}

	url, err := rel.FindAssetURL()
	if err != nil {
		t.Fatalf("unexpected error finding asset URL: %v", err)
	}
	if url != "https://example.com/"+expectedAsset {
		t.Errorf("url = %q, want %q", url, "https://example.com/"+expectedAsset)
	}

	// Missing asset returns descriptive error
	emptyRel := &Release{
		TagName: "v5.0.0",
		Assets:  nil,
	}
	_, err = emptyRel.FindAssetURL()
	if err == nil {
		t.Fatal("expected error for empty release assets")
	}
}

func TestCleanupDir(t *testing.T) {
	dir := t.TempDir()
	oldFile := filepath.Join(dir, "kari.exe.old")
	tmpFile := filepath.Join(dir, "kari.tmp")
	unrelated := filepath.Join(dir, "kari-config.json")

	_ = os.WriteFile(oldFile, []byte("old binary"), 0o755)
	_ = os.WriteFile(tmpFile, []byte("tmp binary"), 0o755)
	_ = os.WriteFile(unrelated, []byte("keep me"), 0o644)

	CleanupDir(dir, "kari.exe")

	if _, err := os.Stat(oldFile); !os.IsNotExist(err) {
		t.Errorf("expected %s to be deleted", oldFile)
	}
	if _, err := os.Stat(tmpFile); !os.IsNotExist(err) {
		t.Errorf("expected %s to be deleted", tmpFile)
	}
	if _, err := os.Stat(unrelated); err != nil {
		t.Errorf("expected %s to be kept, got err: %v", unrelated, err)
	}
}
