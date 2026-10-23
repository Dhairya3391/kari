//go:build !android

package player

import (
	"os/exec"
	"testing"
)

func TestMPVCommandAvailable(t *testing.T) {
	bin, prefix := mpvCommand()
	p := &MPVPlayer{}

	if bin == "" {
		if p.Available() {
			t.Errorf("p.Available() = true when mpvCommand() returned empty")
		}
		return
	}

	if !p.Available() {
		t.Errorf("p.Available() = false when mpvCommand() returned %q", bin)
	}

	if bin == "mpv" {
		if len(prefix) != 0 {
			t.Errorf("native mpv should not have prefix args, got: %v", prefix)
		}
	} else if bin == "flatpak" {
		if len(prefix) != 2 || prefix[0] != "run" || prefix[1] != "io.mpv.Mpv" {
			t.Errorf("flatpak mpv expected ['run', 'io.mpv.Mpv'], got: %v", prefix)
		}
	}
}

func TestMPVPlayerName(t *testing.T) {
	p := &MPVPlayer{}
	if p.Name() != "mpv" {
		t.Errorf("p.Name() = %q, want 'mpv'", p.Name())
	}
}

func TestLookPathOrFlatpakConsistency(t *testing.T) {
	// If native mpv is in PATH, mpvCommand must choose it.
	if _, err := exec.LookPath("mpv"); err == nil {
		bin, prefix := mpvCommand()
		if bin != "mpv" || len(prefix) != 0 {
			t.Errorf("expected native mpv, got bin=%q prefix=%v", bin, prefix)
		}
	}
}
