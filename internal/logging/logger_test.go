package logging

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRotateIfOversized_RotatesWhenOverLimit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "kari.log")
	oversized := strings.Repeat("x", maxLogSizeBytes+1)
	if err := os.WriteFile(path, []byte(oversized), 0o644); err != nil {
		t.Fatalf("seed log file: %v", err)
	}

	rotateIfOversized(path)

	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("expected oversized log to be moved aside, but %s still exists (err=%v)", path, err)
	}
	backup := path + ".1"
	data, err := os.ReadFile(backup)
	if err != nil {
		t.Fatalf("expected rotated backup at %s: %v", backup, err)
	}
	if string(data) != oversized {
		t.Fatalf("rotated backup content mismatch")
	}
}

func TestRotateIfOversized_LeavesSmallFileAlone(t *testing.T) {
	path := filepath.Join(t.TempDir(), "kari.log")
	if err := os.WriteFile(path, []byte("small log"), 0o644); err != nil {
		t.Fatalf("seed log file: %v", err)
	}

	rotateIfOversized(path)

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("expected untouched log file: %v", err)
	}
	if string(data) != "small log" {
		t.Fatalf("log file content changed unexpectedly")
	}
	if _, err := os.Stat(path + ".1"); !os.IsNotExist(err) {
		t.Fatalf("did not expect a rotated backup for a small file")
	}
}

func TestRotateIfOversized_MissingFileIsNoop(t *testing.T) {
	path := filepath.Join(t.TempDir(), "does-not-exist.log")
	rotateIfOversized(path) // must not panic
}
func TestShouldMirrorStderr_NilStderr(t *testing.T) {
	if ShouldMirrorStderr(os.Stdout, nil) {
		t.Fatal("expected nil stderr to return false")
	}
}

func TestShouldMirrorStderr_FileStderr(t *testing.T) {
	tmpFile, err := os.CreateTemp(t.TempDir(), "stderr-test")
	if err != nil {
		t.Fatalf("create temp file: %v", err)
	}
	defer tmpFile.Close()

	if !ShouldMirrorStderr(os.Stdout, tmpFile) {
		t.Fatal("expected file stderr to return true (safe to mirror)")
	}
}

func TestShouldMirrorStderr_PipeStderr(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("create pipe: %v", err)
	}
	defer r.Close()
	defer w.Close()

	if !ShouldMirrorStderr(os.Stdout, w) {
		t.Fatal("expected pipe stderr to return true (safe to mirror)")
	}
}

func TestInit_KariLogStderr_RedirectedFile(t *testing.T) {
	ResetForTesting()
	defer ResetForTesting()

	logDir := t.TempDir()
	logPath := filepath.Join(logDir, "kari.log")
	t.Setenv("KARI_LOG_FILE", logPath)
	t.Setenv("KARI_LOG_STDERR", "1")

	if err := Init(false); err != nil {
		t.Fatalf("Init failed: %v", err)
	}
	defer func() { _ = Close() }()

	// In `go test`, stdout/stderr is normally redirected to a test runner pipe/buffer.
	// Regardless of whether stderr is a pipe in `go test` or a terminal, StderrSuppressed
	// must match whether shouldMirrorStderr was false.
	expectedSuppressed := !ShouldMirrorStderr(os.Stdout, os.Stderr)
	if StderrSuppressed() != expectedSuppressed {
		t.Fatalf("StderrSuppressed() = %v, want %v", StderrSuppressed(), expectedSuppressed)
	}
}
