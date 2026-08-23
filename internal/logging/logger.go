// Package logging provides Kari's structured application log.
//
// The API is structured-first: messages are static phrases and data rides
// as typed key/value pairs, so output is greppable, machine-parseable, and
// free of format-string bugs.
//
//	logging.Debug("search done", "mode", mode, "query", q, "results", n)
//
// Subsystems scope their lines with With instead of embedding names in the
// message:
//
//	log := logging.With("provider", c.Name())
//	log.Debug("fetch start", "tmdbID", id)
//
// Levels follow docs/CODEBASE.md §8: Debug for execution tracing and
// expected fan-out failures, Info for lifecycle events, Warn for failures a
// user might care about without debug enabled, Error for operations that
// aborted.
//
// Output goes to ~/.config/kari/kari.log (rotated at 10 MB, one backup).
// Environment overrides: KARI_LOG_DEBUG forces debug level, KARI_LOG_STDERR
// mirrors to stderr, KARI_LOG_FORMAT_JSON emits machine-readable JSON.
package logging

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"
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

// maxLogSizeBytes bounds kari.log so a long-running or frequently-launched
// install doesn't grow it forever. Once it crosses this size, the previous
// file is kept as one rotated backup (kari.log.1) and a fresh file starts —
// simple size-based rotation, appropriate for a single local diagnostic log.
const maxLogSizeBytes = 10 * 1024 * 1024 // 10MB

func init() {
	// No-op logger so callers are safe before Init runs.
	logger.Store(slog.New(slog.NewTextHandler(io.Discard, nil)))
}

// Init opens the rotating log file and installs it as the global sink.
// Safe to call once per process; later calls are no-ops.
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

	rotateIfOversized(path)

	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("open log file: %w", err)
	}

	level := slog.LevelInfo
	if debug || envBool("KARI_LOG_DEBUG") {
		level = slog.LevelDebug
	}

	opts := &slog.HandlerOptions{
		Level: level,
		// Source locations only matter while debugging; keeping them off in
		// normal operation keeps lines short and diff-friendly.
		AddSource: level == slog.LevelDebug,
	}

	var writer io.Writer = f
	if envBool("KARI_LOG_STDERR") {
		writer = io.MultiWriter(f, os.Stderr)
	}

	var l *slog.Logger
	if envBool("KARI_LOG_FORMAT_JSON") {
		l = slog.New(slog.NewJSONHandler(writer, opts))
	} else {
		l = slog.New(slog.NewTextHandler(writer, opts))
	}
	logger.Store(l)
	slog.SetDefault(l)
	logFile = f
	logPath = path

	logStartup(f)
	initialized = true

	return nil
}

// Logger returns the active base logger. Prefer the package-level
// Debug/Info/Warn/Error helpers or a With-scoped handle over holding this.
func Logger() *slog.Logger { return logger.Load() }

// dynamicHandler delegates log evaluation to the active root logger at call time,
// ensuring package-level loggers created via logging.With during package init
// route to the configured logger sink once Init runs.
type dynamicHandler struct {
	attrs  []slog.Attr
	groups []string
}

func (h *dynamicHandler) Enabled(ctx context.Context, level slog.Level) bool {
	cur := logger.Load()
	if cur == nil {
		return false
	}
	return cur.Handler().Enabled(ctx, level)
}

func (h *dynamicHandler) Handle(ctx context.Context, r slog.Record) error {
	cur := logger.Load()
	if cur == nil {
		return nil
	}
	target := cur.Handler()
	for _, g := range h.groups {
		target = target.WithGroup(g)
	}
	if len(h.attrs) > 0 {
		target = target.WithAttrs(h.attrs)
	}
	return target.Handle(ctx, r)
}

func (h *dynamicHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	newAttrs := make([]slog.Attr, 0, len(h.attrs)+len(attrs))
	newAttrs = append(newAttrs, h.attrs...)
	newAttrs = append(newAttrs, attrs...)
	return &dynamicHandler{
		attrs:  newAttrs,
		groups: append([]string(nil), h.groups...),
	}
}

func (h *dynamicHandler) WithGroup(name string) slog.Handler {
	newGroups := make([]string, 0, len(h.groups)+1)
	newGroups = append(newGroups, h.groups...)
	newGroups = append(newGroups, name)
	return &dynamicHandler{
		attrs:  append([]slog.Attr(nil), h.attrs...),
		groups: newGroups,
	}
}

// With returns a logger bound with permanent key/value pairs — the
// idiomatic way to scope every line of a subsystem:
//
//	var log = logging.With("component", "service.media")
func With(args ...any) *slog.Logger {
	return slog.New(&dynamicHandler{}).With(args...)
}
// Debug logs at debug level; args are alternating key/value pairs.
func Debug(msg string, args ...any) { logger.Load().Debug(msg, args...) }

// Info logs at info level.
func Info(msg string, args ...any) { logger.Load().Info(msg, args...) }

// Warn logs at warn level.
func Warn(msg string, args ...any) { logger.Load().Warn(msg, args...) }

// Error logs at error level.
func Error(msg string, args ...any) { logger.Load().Error(msg, args...) }

// logStartup writes the session banner as raw bytes so slog never mangles it.
func logStartup(w io.Writer) {
	if envBool("KARI_LOG_FORMAT_JSON") {
		return
	}
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
