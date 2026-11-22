package util

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestAtomicWriteFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")

	if err := AtomicWriteFile(path, []byte("first"), 0o600); err != nil {
		t.Fatalf("initial write: %v", err)
	}
	if err := AtomicWriteFile(path, []byte("second"), 0o600); err != nil {
		t.Fatalf("overwrite: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "second" {
		t.Fatalf("content = %q, %v; want second", data, err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("perm = %o, want 600", info.Mode().Perm())
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Errorf("left %d files in dir, want 1 (no temp residue)", len(entries))
	}
}

func TestAtomicWriteFileConcurrent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	const writers = 16

	var wg sync.WaitGroup
	errs := make(chan error, writers)
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			data := bytes.Repeat([]byte(fmt.Sprintf("%02d", n)), 256)
			errs <- AtomicWriteFile(path, data, 0o600)
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent write: %v", err)
		}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) != 512 {
		t.Fatalf("content length = %d, want 512 (one complete write)", len(data))
	}
}

func TestAtomicWriteFileMissingDir(t *testing.T) {
	path := filepath.Join(t.TempDir(), "absent", "state.json")
	if err := AtomicWriteFile(path, []byte("x"), 0o600); err == nil {
		t.Fatal("writing into a missing directory must fail")
	}
}

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
