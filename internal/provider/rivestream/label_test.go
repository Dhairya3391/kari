package rivestream

import "testing"

func TestNormalizeQualityLanguage(t *testing.T) {
	tests := []struct {
		in           string
		wantQuality  string
		wantLanguage string
	}{
		{"720p | English", "720p | English", "English"},
		{"720p | हिन्दी", "720p | Hindi", "Hindi"},
		{"1080p | தமிழ்", "1080p | Tamil", "Tamil"},
		{"480 | hin", "480 | Hindi", "Hindi"}, // bare-integer + code form
		{"HLS", "HLS", ""},                    // no suffix
		{"dcloud", "dcloud", ""},
	}
	for _, tt := range tests {
		q, lang := normalizeQualityLanguage(tt.in)
		if q != tt.wantQuality || lang != tt.wantLanguage {
			t.Errorf("normalizeQualityLanguage(%q) = (%q, %q), want (%q, %q)",
				tt.in, q, lang, tt.wantQuality, tt.wantLanguage)
		}
		for _, r := range q {
			if r > 127 {
				t.Fatalf("quality %q contains non-ASCII rune %q", q, r)
			}
		}
	}
}

// TestAllEmittedLanguagesAreASCIIWords is a coarse guard: whatever the
// mapper produces must be plain English words, never raw upstream tags.
func TestAllEmittedLanguagesAreASCIIWords(t *testing.T) {
	for _, in := range []string{"a | हिन्दी", "b | తెలుగు", "c | മലയാളം", "d | 한국어"} {
		q, _ := normalizeQualityLanguage(in)
		for _, r := range q {
			if r > 127 {
				t.Fatalf("quality %q (from %q) is not ASCII-clean", q, in)
			}
		}
	}
}
