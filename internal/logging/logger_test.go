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
