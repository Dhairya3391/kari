package provider

import (
	"fmt"
	"testing"
)

// TestAudioUnavailableClassification proves the verified-missing-track
// sentinel classifies distinctly and never trips the breaker.
func TestAudioUnavailableClassification(t *testing.T) {
	err := fmt.Errorf("no dub servers: %w", ErrAudioUnavailable)
	if KindOf(err) != KindAudioUnavailable {
		t.Errorf("KindOf = %q, want audio_unavailable", KindOf(err))
	}
	if BreakerFailure(err) {
		t.Error("audio-unavailable must not count as provider sickness")
	}
}
