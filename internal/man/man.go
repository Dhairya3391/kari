// Package man manages installation of the kari(1) manual page for Unix systems.
package man

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"

	"kari"
	"kari/internal/logging"
)

var manLog = logging.With("component", "man")

// Page is the troff/groff source of the kari(1) manual page.
var Page = kari.ManPage

// CandidateDirs returns the search list of manual section 1 directories in
// order of preference for the current platform. Windows and Android return nil.
func CandidateDirs() []string {
	if runtime.GOOS == "windows" || runtime.GOOS == "android" {
		return nil
	}

	var dirs []string

	// System-wide man directory (standard on macOS and Linux).
	dirs = append(dirs, "/usr/local/share/man/man1")

	// Homebrew on macOS (Apple Silicon).
	if runtime.GOOS == "darwin" {
		dirs = append(dirs, "/opt/homebrew/share/man/man1")
	}

	// User-local XDG data directory (always writable by non-root users).
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		dirs = append(dirs, filepath.Join(home, ".local", "share", "man", "man1"))
	}

	return dirs
}

// IsInstalled returns true if kari.1 is present in any candidate directory and
// its content matches the embedded Page.
func IsInstalled() bool {
	for _, dir := range CandidateDirs() {
		path := filepath.Join(dir, "kari.1")
		if data, err := os.ReadFile(path); err == nil && bytes.Equal(data, Page) {
			return true
		}
	}
	return false
}

// EnsureInstalled writes the embedded man page to the first writable candidate
// directory if it is not already installed or if the installed copy is outdated.
// It is safe to call repeatedly; if the page is already current, it is a no-op.
// Returns the installed path on success, or an empty string if installation was
// skipped (e.g. non-Unix OS or no writable candidate found).
func EnsureInstalled() (string, error) {
	if runtime.GOOS == "windows" || runtime.GOOS == "android" {
		return "", nil
	}

	// Check if already up to date in any candidate directory.
	for _, dir := range CandidateDirs() {
		dest := filepath.Join(dir, "kari.1")
		if data, err := os.ReadFile(dest); err == nil && bytes.Equal(data, Page) {
			manLog.Debug("man page already current", "path", dest)
			return dest, nil
		}
	}

	// Find the best writable candidate directory.
	var targetDir string
	for _, dir := range CandidateDirs() {
		if err := os.MkdirAll(dir, 0o755); err == nil {
			// Verify write permission by creating a temporary file.
			testFile := filepath.Join(dir, fmt.Sprintf(".kari-man-test-%d", os.Getpid()))
			if tf, err := os.OpenFile(testFile, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o600); err == nil {
				_ = tf.Close()
				_ = os.Remove(testFile)
				targetDir = dir
				break
			}
		}
	}

	if targetDir == "" {
		manLog.Debug("no writable man directory found; skipping man page install")
		return "", nil
	}

	dest := filepath.Join(targetDir, "kari.1")
	tmp := dest + fmt.Sprintf(".tmp.%d", os.Getpid())

	if err := os.WriteFile(tmp, Page, 0o644); err != nil {
		return "", fmt.Errorf("write temporary man page: %w", err)
	}

	if err := os.Rename(tmp, dest); err != nil {
		_ = os.Remove(tmp)
		return "", fmt.Errorf("install man page to %s: %w", dest, err)
	}

	manLog.Info("installed man page", "path", dest)

	// On Linux, rebuild the whatis/mandb cache in the background (fire-and-forget).
	if runtime.GOOS == "linux" {
		if mandbPath, err := exec.LookPath("mandb"); err == nil {
			go func() {
				_ = exec.Command(mandbPath, "-q").Run()
			}()
		}
	}

	return dest, nil
}
