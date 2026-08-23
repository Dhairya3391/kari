package logging

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Close flushes and closes the log sink after writing the shutdown banner;
// safe when never initialized.
func Close() error {
	mu.Lock()
	defer mu.Unlock()

	if logFile != nil {
		logShutdown(logFile)
		err := logFile.Close()
		logFile = nil
		return err
	}
	return nil
}

// logShutdown writes the session-end banner as raw bytes.
func logShutdown(w io.Writer) {
	if envBool("KARI_LOG_FORMAT_JSON") {
		return
	}
	now := time.Now().Format("2006-01-02 15:04:05")
	lines := []string{
		"",
		sepLine,
		"",
		headerStart,
		"║                                                              ║",
		"║                    👋 Kari Stopped                         ║",
		"║                                                              ║",
		headerEnd,
		"stopped at " + now,
		"",
	}
	for _, l := range lines {
		fmt.Fprintln(w, l)
	}
}

// Path returns the active log file path (empty before Init).
func Path() string { return logPath }

// rotateIfOversized moves an existing oversized log file to path+".1"
// (replacing any prior backup) before Init opens path fresh. Best-effort:
// if stat or rename fails (e.g. permissions), it silently leaves the
// existing file in place rather than blocking startup or losing logs.
func rotateIfOversized(path string) {
	info, err := os.Stat(path)
	if err != nil || info.Size() < maxLogSizeBytes {
		return
	}
	_ = os.Remove(path + ".1")
	_ = os.Rename(path, path+".1")
}

// resolveLogPath honors KARI_LOG_FILE (relative paths resolve against the
// working directory) and falls back to ~/.config/kari/kari.log.
func resolveLogPath() (string, error) {
	if path := firstEnv("KARI_LOG_FILE"); path != "" {
		if filepath.IsAbs(path) {
			return path, nil
		}
		cwd, err := os.Getwd()
		if err != nil {
			return "", err
		}
		return filepath.Join(cwd, path), nil
	}

	home, err := os.UserHomeDir()
	if err != nil {
		home = os.Getenv("HOME")
		if home == "" {
			return "", fmt.Errorf("resolve user home directory: %w", err)
		}
	}
	return filepath.Join(home, ".config", "kari", "kari.log"), nil
}

// envBool reports whether any of the named environment variables holds a
// truthy value ("1", "true", "yes", "y", "on").
func envBool(keys ...string) bool {
	for _, key := range keys {
		v := strings.ToLower(strings.TrimSpace(os.Getenv(key)))
		switch v {
		case "1", "true", "yes", "y", "on":
			return true
		}
	}
	return false
}

// firstEnv returns the first non-empty value among the given environment
// variable names.
func firstEnv(keys ...string) string {
	for _, key := range keys {
		if v := strings.TrimSpace(os.Getenv(key)); v != "" {
			return v
		}
	}
	return ""
}
