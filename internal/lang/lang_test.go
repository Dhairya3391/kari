package lang

import (
	"slices"
	"testing"
)

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
		{"Hindi", "hi"},                // English name form
		{"hin", "hi"},                  // 3-letter code
		{"EN", "en"},                   // case folding
		{"zh-cn", "zh"},                // regional variant
		{"Protuguese (BR)", "pt"},      //nolint:misspell // typo form, verbatim
		{"english (cr english)", "en"}, // source annotation, stripped
		{"English (US)", "en"},         // region annotation, stripped
		{"may", "ms"},                  // ISO 639-2/B Malay code
		{"disabled", "off"},            // subtitle off alias
		{"none", "off"},                // subtitle off alias
		{"Estonian", "et"},
		{"est", "et"},
	}
	for _, tt := range tests {
		if got := Normalize(tt.raw); got != tt.want {
			t.Errorf("Normalize(%q)=%q want %q", tt.raw, got, tt.want)
		}
	}
}

func TestFromLabel(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{"arabic hi marker", "Arabic Hi5", "ar"},
		{"brazilian ordinal", "Brazilian10", "pt"},
		{"bare hindi code", "hi", "hi"},
		{"bare sdh code stays unknown", "sdh", "sdh"},
		{"brazilian plain", "Brazilian", "pt"},
		{"english numbered annotation", "English (2)", "en"},
		{"english source annotation", "english (cr english)", "en"},
		{"english dot separated tags", "English · [SoM] English", "en"},
		{"hindi devanagari alt", "हिन्दी", "hi"},
		{"hindi devanagari", "हिंदी", "hi"},
		{"english plain", "English", "en"},
		{"filipino plain", "Filipino", "fil"},
		{"filipino tagalog annotation", "Filipino (tagalog)", "fil"},
		{"portuguese region annotation", "Portuguese (BR)", "pt"},
		{"chinese traditional annotation", "Chinese (traditional)", "zh"},
		{"trailing hi marker", "English Hi", "en"},
		{"trailing uppercase hi marker", "English HI5", "en"},
		{"trailing sdh marker", "English SDH", "en"},
		{"attached trailing number", "Arabic2", "ar"},
		{"bracket tag without separator", "English [SoM]", "en"},
		{"whitespace trimmed", "  English  ", "en"},
		{"hyphenated code preserved", "es-419", "es-419"},
		{"hyphenated alias", "pt-BR", "pt"},
		{"unknown cleaned lowercase", "Klingon", "klingon"},
		{"empty", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := FromLabel(tt.raw); got != tt.want {
				t.Errorf("FromLabel(%q)=%q want %q", tt.raw, got, tt.want)
			}
		})
	}
}

func TestCanonicalize(t *testing.T) {
	tests := []struct {
		name string
		list []string
		want []string
	}{
		{
			name: "normalizes drops empties and dedupes in order",
			list: []string{"English", "en", "english (cr english)", "", "  ", "Filipino", "fil", "tgl", " Portuguese (BR) "},
			want: []string{"en", "fil", "pt"},
		},
		{
			name: "keeps unknown codes first seen",
			list: []string{"Klingon", "klingon", "xx", "en"},
			want: []string{"klingon", "xx", "en"},
		},
		{
			name: "empty input",
			list: nil,
			want: nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Canonicalize(tt.list); !slices.Equal(got, tt.want) {
				t.Errorf("Canonicalize(%q)=%q want %q", tt.list, got, tt.want)
			}
		})
	}
}

func TestFromCodeOrLabel(t *testing.T) {
	tests := []struct {
		name  string
		code  string
		label string
		want  string
	}{
		{"finnish label keeps fi", "fi", "Finnish", "fi"},
		{"filipino label overrides fi", "fi", "Filipino", "fil"},
		{"agreeing label", "en", "English", "en"},
		{"curated label beats bad code", "zz", "English", "en"},
		{"empty label uses code", "pt-BR", "", "pt"},
		{"hyphenated code label empty", "es-419", "", "es-419"},
		{"unknown label falls back to code", "xx", "Klingon", "xx"},
		{"empty label bare fi", "fi", "", "fi"},
		{"curated label beats fi code", "fi", "Hindi", "hi"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := FromCodeOrLabel(tt.code, tt.label); got != tt.want {
				t.Errorf("FromCodeOrLabel(%q, %q)=%q want %q", tt.code, tt.label, got, tt.want)
			}
		})
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
		{"off", "Off"},
		{"th", "Thai"},
		{"vi", "Vietnamese"},
		{"ms", "Malay"},
		{"may", "Malay"},
		{"et", "Estonian"},
		{"estonian", "Estonian"},
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
	for _, code := range SubtitleOptions {
		name := Name(code)
		if name == code || name == "" {
			t.Errorf("subtitle option %q has no display name", code)
		}
	}
}

// TestSubtitleOptionsCoverNames keeps the settings picker in sync with the
// display-name table: a language Name can render must also be selectable.
func TestSubtitleOptionsCoverNames(t *testing.T) {
	options := make(map[string]struct{}, len(SubtitleOptions))
	for _, code := range SubtitleOptions {
		options[code] = struct{}{}
	}
	for code := range names {
		if _, ok := options[code]; !ok {
			t.Errorf("language %q is missing from SubtitleOptions", code)
		}
	}
}
