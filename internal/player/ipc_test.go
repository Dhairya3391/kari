package player

import "testing"

func TestPlaybackStats_LoadedState(t *testing.T) {
	ps := newPlaybackStats()

	if ps.playing() {
		t.Fatalf("expected playing() to be false initially")
	}

	// When MPV opens media at position 0.0s, playing() must return true so readiness check passes
	ps.update(0.0, 0.0, true)
	if !ps.playing() {
		t.Fatalf("expected playing() to be true when loaded is true, even at position 0.0s")
	}
}
