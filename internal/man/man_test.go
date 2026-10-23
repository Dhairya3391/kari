package man

import (
	"bytes"
	"path/filepath"
	"runtime"
	"testing"
)

// TestPageContent ensures the embedded man page is populated with valid troff/groff.
func TestPageContent(t *testing.T) {
	if len(Page) == 0 {
		t.Fatal("embedded man page is empty")
	}
	if !bytes.HasPrefix(Page, []byte(".\\\" kari.1")) && !bytes.Contains(Page, []byte(".TH KARI 1")) {
		t.Fatal("embedded man page missing expected .TH header")
	}
}

// TestCandidateDirs verifies that candidate directories are properly resolved on Unix.
func TestCandidateDirs(t *testing.T) {
	dirs := CandidateDirs()
	if runtime.GOOS == "windows" || runtime.GOOS == "android" {
		if len(dirs) != 0 {
			t.Errorf("expected no candidate dirs on %s, got %v", runtime.GOOS, dirs)
		}
		return
	}

	if len(dirs) == 0 {
		t.Fatal("expected at least one candidate man directory on Unix")
	}

	foundMan1 := false
	for _, d := range dirs {
		if filepath.Base(d) == "man1" {
			foundMan1 = true
			break
		}
	}
	if !foundMan1 {
		t.Errorf("expected a man1 subdirectory in candidates: %v", dirs)
	}
}
