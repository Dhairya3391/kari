package logging

import (
	"io"
	"log/slog"
	"os"
)

// ResetForTesting cleans up all internal logging state and restores the discard
// sink so tests can re-run Init under different environment configurations.
func ResetForTesting() {
	mu.Lock()
	defer mu.Unlock()
	initialized = false
	isDebug = false
	stderrSuppressed = false
	if logFile != nil {
		_ = logFile.Close()
		logFile = nil
	}
	logPath = ""
	logger.Store(slog.New(slog.NewTextHandler(io.Discard, nil)))
}

// ShouldMirrorStderr exposes shouldMirrorStderr for test verification.
func ShouldMirrorStderr(stdout, stderr *os.File) bool {
	return shouldMirrorStderr(stdout, stderr)
}
