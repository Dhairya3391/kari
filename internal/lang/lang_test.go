package lang

import "testing"

func TestNormalizeFoldsNativeScripts(t *testing.T) {
	tests := []struct {
		raw  string
		want string
	}{
		{"हिन्दी", "hi"},
		{"हिंदी", "hi"},
		{"தமிழ்", "ta"},
		{"తెలుగు", "te"},
		{"മലയാളം", "ml"},
		{"ಕನ್ನಡ", "kn"},
		{"मराठी", "mr"},
		{"ગુજરાતી", "gu"},
		{"اردو", "ur"},
		{"日本語", "ja"},
		{"Hindi", "hi"},           // English name form
		{"hin", "hi"},             // 3-letter code
		{"EN", "en"},              // case folding
		{"zh-cn", "zh"},           // regional variant
		{"Protuguese (BR)", "pt"}, // VidKing typo, verbatim
	}
	for _, tt := range tests {
		if got := Normalize(tt.raw); got != tt.want {
			t.Errorf("Normalize(%q)=%q want %q", tt.raw, got, tt.want)
		}
	}
}

func TestNameAlwaysRendersEnglish(t *testing.T) {
	tests := []struct {
		raw  string
		want string
	}{
		{"hi", "Hindi"},
		{"हिन्दी", "Hindi"},
		{"tamil", "Tamil"},
		{"gu", "Gujarati"},
		{"ja", "Japanese"},
		{"en", "English"},
	}
	for _, tt := range tests {
		if got := Name(tt.raw); got != tt.want {
			t.Errorf("Name(%q)=%q want %q", tt.raw, got, tt.want)
		}
	}
	if got := Name(""); got != "Unknown" {
		t.Errorf("Name(\"\")=%q want Unknown", got)
	}
	// Unmapped tags surface as the uppercased code — never silently blank.
	if got := Name("xx"); got != "XX" {
		t.Errorf("Name(\"xx\")=%q want XX", got)
	}
}

func TestSubtitleOptionsAllResolvable(t *testing.T) {
	// Every selectable subtitle language must render as a proper English
	// name; a gap here shows raw codes in Settings.
	for _, code := range SubtitleOptions {
		name := Name(code)
		if name == code || name == "" {
			t.Errorf("subtitle option %q has no display name", code)
		}
	}
}
