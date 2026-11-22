package util

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"
)

// AtomicWriteFile writes data to a unique temp file in the same directory as
// path and renames it into place, so a crash or power loss mid-write leaves
// either the old contents or the new ones, never a truncated/corrupt file.
// A unique temp name lets concurrent writers coexist; the file is synced
// before the rename so the replacement survives a power loss.
func AtomicWriteFile(path string, data []byte, perm os.FileMode) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".kari-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := f.Name()
	defer func() { _ = os.Remove(tmpPath) }()

	if err := f.Chmod(perm); err != nil {
		_ = f.Close()
		return err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}

// NormalizeSpace collapses runs of whitespace into single spaces and trims
// the ends, normalizing titles pulled from APIs with inconsistent spacing.
func NormalizeSpace(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// OpenBrowser opens url in the platform's default browser. The command is
// started detached; failure to open is reported but never retried.
func OpenBrowser(url string) error {
	if b := os.Getenv("BROWSER"); b != "" {
		if err := exec.Command(b, url).Start(); err == nil {
			return nil
		}
	}
	switch runtime.GOOS {
	case "darwin":
		return exec.Command("open", url).Start()
	case "windows":
		return exec.Command("cmd", "/c", "start", "", url).Start()
	case "android":
		return exec.Command("am", "start", "-a", "android.intent.action.VIEW", "-d", url).Start()
	default:
		if _, err := exec.LookPath("wslview"); err == nil {
			if err := exec.Command("wslview", url).Start(); err == nil {
				return nil
			}
		}
		return exec.Command("xdg-open", url).Start()
	}
}

// PruneDirToSize deletes the oldest files in a flat cache directory
// until its total size fits maxBytes, returning the removed count. A
// missing directory is not an error (nothing cached yet) and
// subdirectories are never touched. Callers run this once at startup so
// image caches stay bounded across sessions without slowing down exit.
func PruneDirToSize(dir string, maxBytes int64) (int, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	type fileStat struct {
		path string
		size int64
		mod  time.Time
	}
	var files []fileStat
	var total int64
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		files = append(files, fileStat{path: filepath.Join(dir, e.Name()), size: info.Size(), mod: info.ModTime()})
		total += info.Size()
	}
	slices.SortFunc(files, func(a, b fileStat) int {
		if a.mod.Before(b.mod) {
			return -1
		}
		if a.mod.After(b.mod) {
			return 1
		}
		return 0
	})
	removed := 0
	for _, f := range files {
		if total <= maxBytes {
			break
		}
		if err := os.Remove(f.path); err != nil {
			continue
		}
		total -= f.size
		removed++
	}
	return removed, nil
}
