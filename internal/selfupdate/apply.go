package selfupdate

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"kari/internal/httpclient"
	"kari/internal/logging"
	"kari/internal/man"
)

var updateLog = logging.With("component", "selfupdate")

// FindAssetURL returns the download URL for the current platform's binary asset from the release.
func (r *Release) FindAssetURL() (string, error) {
	assetName := fmt.Sprintf("kari-%s-%s", runtime.GOOS, runtime.GOARCH)
	if runtime.GOOS == "windows" {
		assetName += ".exe"
	}
	for _, asset := range r.Assets {
		if asset.Name == assetName {
			return asset.BrowserDownloadURL, nil
		}
	}
	return "", fmt.Errorf("could not find binary for %s/%s in release %s", runtime.GOOS, runtime.GOARCH, r.TagName)
}

// ProgressFunc reports download progress (percentage 0-100).
type ProgressFunc func(percent int)

// Apply downloads the release binary for the current platform and replaces the current executable.
// If onProgress is non-nil, it is called periodically with download progress (0-100).
func (r *Release) Apply(onProgress ProgressFunc) error {
	downloadURL, err := r.FindAssetURL()
	if err != nil {
		return err
	}
	return ApplyURL(downloadURL, onProgress)
}

// ApplyURL downloads a binary from the given URL and atomically replaces the current executable.
// Handles Windows file locking, Linux/macOS/Android file permissions, and directory write checks.
func ApplyURL(url string, onProgress ProgressFunc) error {
	exePath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve executable path: %w", err)
	}
	if realPath, err := filepath.EvalSymlinks(exePath); err == nil && realPath != "" {
		exePath = realPath
	}

	// Verify target directory is writable before downloading large payload
	dir := filepath.Dir(exePath)
	testFile := filepath.Join(dir, fmt.Sprintf(".kari-write-test-%d", time.Now().UnixNano()))
	tf, err := os.OpenFile(testFile, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o600)
	if err != nil {
		if os.IsPermission(err) {
			return fmt.Errorf("permission denied writing to %s (run with sudo or install in a user-writable directory): %w", dir, err)
		}
		return fmt.Errorf("update directory %s is not writable: %w", dir, err)
	}
	_ = tf.Close()
	_ = os.Remove(testFile)

	client := httpclient.NewWithTimeout(120 * time.Second)
	resp, err := client.Get(url)
	if err != nil {
		return fmt.Errorf("fetch update binary: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("failed to download binary: HTTP %d", resp.StatusCode)
	}

	total, _ := strconv.Atoi(resp.Header.Get("Content-Length"))

	tmpExt := ".tmp"
	oldExt := ".old"
	if runtime.GOOS == "windows" {
		tmpExt = ".tmp.exe"
		oldExt = ".old.exe"
	}

	tmpPath := exePath + tmpExt
	_ = os.Remove(tmpPath)

	f, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return fmt.Errorf("create temporary update binary: %w", err)
	}

	var written atomic.Int64
	done := make(chan struct{})
	if onProgress != nil {
		go func() {
			tick := time.NewTicker(200 * time.Millisecond)
			defer tick.Stop()
			for {
				select {
				case <-tick.C:
					pct := 0
					if total > 0 {
						pct = int(written.Load() * 100 / int64(total))
					}
					onProgress(pct)
				case <-done:
					return
				}
			}
		}()
	}

	var writer io.Writer = f
	if onProgress != nil {
		writer = io.MultiWriter(f, &progressWriter{written: &written})
	}
	_, err = io.Copy(writer, resp.Body)
	close(done)

	if err != nil {
		_ = f.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("write update binary: %w", err)
	}
	if err := f.Close(); err != nil {
		updateLog.Warn("temp file close failed", "err", err)
	}

	// Ensure execute permissions on Unix (Linux, macOS, Android/Termux) regardless of umask
	if runtime.GOOS != "windows" {
		if err := os.Chmod(tmpPath, 0o755); err != nil {
			updateLog.Warn("chmod update binary failed", "err", err)
		}
	}

	// Prepare oldPath. On Windows, if oldPath already exists, remove it first.
	oldPath := exePath + oldExt
	_ = os.Remove(oldPath)
	// If oldPath still exists (locked by another process), use a timestamped backup name
	if _, statErr := os.Stat(oldPath); statErr == nil {
		oldPath = fmt.Sprintf("%s.%d%s", exePath, time.Now().UnixNano(), oldExt)
	}

	if err := os.Rename(exePath, oldPath); err != nil {
		updateLog.Error("current binary rename failed", "err", err)
		_ = os.Remove(tmpPath)
		return fmt.Errorf("backup current binary: %w", err)
	}

	if err := os.Rename(tmpPath, exePath); err != nil {
		if e := os.Rename(oldPath, exePath); e != nil {
			updateLog.Error("rollback rename failed", "err", e)
		}
		_ = os.Remove(tmpPath)
		return fmt.Errorf("swap binary: %w", err)
	}

	// Try removing old binary immediately. On Unix this succeeds; on Windows it is deferred
	// to next startup cleanup because the running process holds an active lock.
	if err := os.Remove(oldPath); err != nil {
		updateLog.Debug("old binary removal deferred to next run", "file", oldPath, "err", err)
	}

	// Best-effort man page installation/update on Unix systems.
	if manPath, err := man.EnsureInstalled(); err != nil {
		updateLog.Debug("man page update skipped", "err", err)
	} else if manPath != "" {
		updateLog.Info("updated manual page", "path", manPath)
	}

	return nil
}

// CleanupOldExecutables removes any leftover .old or .tmp binaries from previous updates.
// On Windows, the running binary cannot be deleted until the process exits, so removal
// is deferred to the subsequent run.
func CleanupOldExecutables() {
	exePath, err := os.Executable()
	if err != nil {
		return
	}
	if realPath, err := filepath.EvalSymlinks(exePath); err == nil && realPath != "" {
		exePath = realPath
	}
	CleanupDir(filepath.Dir(exePath), filepath.Base(exePath))
}

// CleanupDir removes any leftover .old or .tmp binaries matching prefix in dir.
func CleanupDir(dir, base string) {
	ext := filepath.Ext(base)
	prefix := base
	if ext != "" {
		prefix = strings.TrimSuffix(base, ext)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if strings.HasPrefix(name, prefix) && (strings.Contains(name, ".old") || strings.Contains(name, ".tmp")) {
			fullPath := filepath.Join(dir, name)
			if err := os.Remove(fullPath); err == nil {
				updateLog.Debug("cleaned up leftover update binary", "file", name)
			}
		}
	}
}

type progressWriter struct {
	written *atomic.Int64
}

func (pw *progressWriter) Write(p []byte) (int, error) {
	n := len(p)
	pw.written.Add(int64(n))
	return n, nil
}
