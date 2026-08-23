package moviebox

import "testing"

// TestMovieboxDisplayCoversOwnCodes guards the private-code table: every
// code MovieBox can emit must resolve through its own display names (not
// the shared mapper, which would render "esla" as "ESLA").
func TestMovieboxDisplayCoversOwnCodes(t *testing.T) {
	for _, l := range audioLanguages {
		got := movieboxDisplay(l.Code)
		if got != l.Display {
			t.Errorf("movieboxDisplay(%q) = %q, want %q", l.Code, got, l.Display)
		}
	}
}

// Unknown codes fall back to the shared mapper rather than leaking raw.
func TestMovieboxDisplayFallback(t *testing.T) {
	if got := movieboxDisplay("hi"); got != "Hindi" {
		t.Errorf("fallback for known ISO code: %q", got)
	}
}

// TestBuildSourcesLanguageUsesStableCode guards the settings-filter
// contract: MediaSource.Language must be the raw AudioLanguage.Code the
// TUI persists, never the display name — otherwise disabling a language
// whose display differs from its code ("esla", "ptbr") silently stops
// filtering its sources.
func TestBuildSourcesLanguageUsesStableCode(t *testing.T) {
	byLang := map[string][]movieboxSourceItem{
		"English": {{URL: "u1", Quality: "720p"}},
		"esla":    {{URL: "u2", Quality: "1080p"}},
		"ptbr":    {{URL: "u3", Quality: "480p"}},
	}
	sources := buildSources(byLang, []string{"English", "esla", "ptbr"}, nil)

	got := make(map[string]string, len(sources))
	for _, s := range sources {
		got[s.URL] = s.Language
	}

	want := map[string]string{"u1": "English", "u2": "esla", "u3": "ptbr"}
	for url, code := range want {
		if got[url] != code {
			t.Errorf("source %q Language = %q, want stable code %q", url, got[url], code)
		}
	}

	// Display name stays in the Quality label for UI rendering.
	for _, s := range sources {
		if s.Language == "esla" && s.Quality != "1080p Spanish (LatAm)" {
			t.Errorf("quality label = %q, want display name rendered", s.Quality)
		}
	}
}
