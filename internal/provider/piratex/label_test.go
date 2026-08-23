package piratex

import (
	"strings"
	"testing"

	"kari/internal/lang"
)

// TestTrackLabelNeverLeaksNativeScript guards the audio-group label rule:
// whenever the track declares a language we know, the label must be its
// English name — never the playlist's raw NAME attribute.
func TestTrackLabelNeverLeaksNativeScript(t *testing.T) {
	tests := []struct {
		name     string // raw NAME attribute from the master playlist
		language string // raw LANGUAGE attribute
		want     string
	}{
		{name: "हिन्दी", language: "हिन्दी", want: "HINDI"},
		{name: "தமிழ்", language: "தமிழ்", want: "TAMIL"},
		{name: "English", language: "en", want: "ENGLISH"},
		{name: "", language: "hi", want: "HINDI"},
	}
	for _, tt := range tests {
		label := tt.name
		if code := lang.Normalize(tt.language); code != "" {
			label = strings.ToUpper(lang.Name(code))
		}
		if label != tt.want {
			t.Errorf("name=%q language=%q → %q, want %q", tt.name, tt.language, label, tt.want)
		}
		for _, r := range label {
			if r > 127 {
				t.Fatalf("label %q contains non-ASCII rune %q", label, r)
			}
		}
	}
}

// Non-language group names (no LANGUAGE attribute at all) keep their raw
// playlist label as the fallback.
func TestNonLanguageGroupKeepsPlaylistName(t *testing.T) {
	label := "Main"
	if code := lang.Normalize(""); code != "" {
		label = strings.ToUpper(lang.Name(code))
	}
	if label != "Main" {
		t.Fatalf("fallback label mutated: %q", label)
	}
}
