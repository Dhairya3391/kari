package util

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestPruneDirToSize(t *testing.T) {
	dir := t.TempDir()
	write := func(name string, size int, age time.Duration) {
		t.Helper()
		data := make([]byte, size)
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
			t.Fatal(err)
		}
		past := time.Now().Add(-age)
		if err := os.Chtimes(filepath.Join(dir, name), past, past); err != nil {
			t.Fatal(err)
		}
	}
	write("old.img", 10, 3*time.Hour)
	write("mid.img", 10, 2*time.Hour)
	write("new.img", 10, time.Hour)

	removed, err := PruneDirToSize(dir, 25)
	if err != nil {
		t.Fatalf("PruneDirToSize failed: %v", err)
	}
	if removed != 1 {
		t.Fatalf("removed = %d, want 1", removed)
	}
	if _, err := os.Stat(filepath.Join(dir, "old.img")); !os.IsNotExist(err) {
		t.Errorf("oldest file should have been evicted")
	}
	for _, name := range []string{"mid.img", "new.img"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("%s should survive: %v", name, err)
		}
	}
}

func TestPruneDirToSizeMissingAndFitting(t *testing.T) {
	if removed, err := PruneDirToSize(filepath.Join(t.TempDir(), "absent"), 10); err != nil || removed != 0 {
		t.Errorf("missing dir = %d, %v; want 0, nil", removed, err)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.img"), make([]byte, 5), 0o644); err != nil {
		t.Fatal(err)
	}
	if removed, err := PruneDirToSize(dir, 100); err != nil || removed != 0 {
		t.Errorf("fitting dir = %d, %v; want 0, nil", removed, err)
	}
}
