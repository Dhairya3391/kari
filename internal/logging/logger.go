package logging

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"
)

var (
	logger      atomic.Pointer[slog.Logger]
	logFile     *os.File
	logPath     string
	mu          sync.Mutex
	isDebug     bool
	initialized bool
)

const (
	headerStart = "╔══════════════════════════════════════════════════════════════╗"
	headerEnd   = "╚══════════════════════════════════════════════════════════════╝"
	sepLine     = "────────────────────────────────────────────────────────────────"
)

func init() {
	// default no-op logger so callers are safe before Init
	noop := slog.New(slog.NewTextHandler(io.Discard, nil))
	logger.Store(noop)
}

func Init(debug bool) error {
	mu.Lock()
	defer mu.Unlock()

	if initialized {
		return nil
	}

	isDebug = debug

	path, err := resolveLogPath()
	if err != nil {
		return err
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create log directory: %w", err)
	}

	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("open log file: %w", err)
	}

	level := slog.LevelInfo
	if debug || envBool("KARI_LOG_DEBUG") {
		level = slog.LevelDebug
	}

	opts := &slog.HandlerOptions{
		Level:     level,
		AddSource: false,
	}

	var writer io.Writer = f
	if envBool("KARI_LOG_STDERR") {
		writer = io.MultiWriter(f, os.Stderr)
	}

	l := slog.New(slog.NewTextHandler(writer, opts))
	logger.Store(l)
	slog.SetDefault(l)
	logFile = f
	logPath = path

	logStartup(f)
	initialized = true

	return nil
}

// logStartup writes the banner as raw bytes to avoid slog prefixes mangling it.
func logStartup(w io.Writer) {
	now := time.Now().Format("2006-01-02 15:04:05")
	lines := []string{
		"",
		headerStart,
		"║                                                              ║",
		"║                    🚀 Kari Starting                        ║",
		"║                                                              ║",
		headerEnd,
		"",
		"started at " + now,
		"log path   " + logPath,
		fmt.Sprintf("debug      %v", isDebug),
		"",
		sepLine,
		"system ready",
		"",
	}
	for _, l := range lines {
		fmt.Fprintln(w, l)
	}
}

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

func logShutdown(w io.Writer) {
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

func Path() string {
	return logPath
}

func Debug(msg string, args ...any) {
	logger.Load().Debug(msg, args...)
}

func Info(msg string, args ...any) {
	logger.Load().Info(msg, args...)
}

func Warn(msg string, args ...any) {
	logger.Load().Warn(msg, args...)
}

func Error(msg string, args ...any) {
	logger.Load().Error(msg, args...)
}

// f-variants kept for convenience but prefer structured calls above.
func Debugf(format string, args ...any) { Debug(fmt.Sprintf(format, args...)) }
func Infof(format string, args ...any)  { Info(fmt.Sprintf(format, args...)) }
func Warnf(format string, args ...any)  { Warn(fmt.Sprintf(format, args...)) }
func Errorf(format string, args ...any) { Error(fmt.Sprintf(format, args...)) }

// Preview truncates text to max runes (not bytes).
func Preview(text string, max int) string {
	if max <= 0 {
		max = 200
	}
	flat := strings.NewReplacer("\n", " ", "\r", " ", "\t", " ").Replace(text)
	flat = strings.TrimSpace(flat)
	if utf8.RuneCountInString(flat) <= max {
		return flat
	}
	runes := []rune(flat)
	return string(runes[:max]) + "..."
}

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
		return "", err
	}
	return filepath.Join(home, ".config", "kari", "kari.log"), nil
}

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

func firstEnv(keys ...string) string {
	for _, key := range keys {
		if v := strings.TrimSpace(os.Getenv(key)); v != "" {
			return v
		}
	}
	return ""
}
