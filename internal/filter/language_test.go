package filter

import (
	"slices"
	"testing"

	"kari/internal/provider"
)

func TestLanguagePreferenceAllows(t *testing.T) {
	cases := []struct {
		name string
		pref LanguagePreference
		code string
		want bool
	}{
		{"zero value allows everything", LanguagePreference{}, "hi", true},
		{"zero value allows untagged", LanguagePreference{}, "", true},
		{"explicit disable by code", NewLanguagePreference(nil, []string{"en"}), "en", false},
		{"explicit disable by display name", NewLanguagePreference(nil, []string{"English"}), "en", false},
		{"disable matches code form", NewLanguagePreference(nil, []string{"English"}), "English", false},
		{"other language still allowed", NewLanguagePreference(nil, []string{"en"}), "hi", true},
		{"unknown code allowed", NewLanguagePreference(nil, []string{"en"}), "xyz", true},
		{"empty code allowed", NewLanguagePreference(nil, []string{"en"}), "", true},
		{"disable wins over enable", NewLanguagePreference([]string{"en"}, []string{"en"}), "en", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.pref.Allows(tc.code); got != tc.want {
				t.Errorf("Allows(%q) = %v, want %v", tc.code, got, tc.want)
			}
		})
	}
}

func TestLanguagePreferencePriority(t *testing.T) {
	pref := NewLanguagePreference([]string{"English", "हिन्दी", "fr"}, nil)
	cases := []struct {
		code string
		want int
	}{
		{"en", 0},
		{"hi", 1},
		{"French", 2},
		{"de", unlistedPriority},
		{"", unlistedPriority},
	}
	for _, tc := range cases {
		if got := pref.Priority(tc.code); got != tc.want {
			t.Errorf("Priority(%q) = %d, want %d", tc.code, got, tc.want)
		}
	}
	if !slices.Equal(pref.Enabled, []string{"en", "hi", "fr"}) {
		t.Errorf("canonical enabled list = %v, want [en hi fr]", pref.Enabled)
	}
}

func TestFilterLanguages(t *testing.T) {
	allowEnglish := LanguagePreference{}
	blockEnglish := NewLanguagePreference(nil, []string{"English"})

	cases := []struct {
		name    string
		mode    provider.ContentType
		src     provider.MediaSource
		pref    LanguagePreference
		wantURL string
	}{
		{
			name:    "default allow keeps declared language",
			mode:    provider.ModeMovies,
			src:     provider.MediaSource{URL: "hi", Language: "Hindi"},
			pref:    allowEnglish,
			wantURL: "hi",
		},
		{
			name:    "explicit disable removes declared language",
			mode:    provider.ModeMovies,
			src:     provider.MediaSource{URL: "en", Language: "English"},
			pref:    blockEnglish,
			wantURL: "",
		},
		{
			name:    "typed languages take precedence over legacy field",
			mode:    provider.ModeMovies,
			src:     provider.MediaSource{URL: "typed", Language: "Hindi", Audio: provider.AudioInfo{Languages: []string{"en"}}},
			pref:    blockEnglish,
			wantURL: "",
		},
		{
			name:    "any enabled declared language keeps source",
			mode:    provider.ModeMovies,
			src:     provider.MediaSource{URL: "dual", Audio: provider.AudioInfo{Languages: []string{"en", "hi"}}},
			pref:    blockEnglish,
			wantURL: "dual",
		},
		{
			name:    "all declared languages disabled removes source",
			mode:    provider.ModeMovies,
			src:     provider.MediaSource{URL: "dual", Audio: provider.AudioInfo{Languages: []string{"en", "English"}}},
			pref:    blockEnglish,
			wantURL: "",
		},
		{
			name:    "untagged on-demand kept by default",
			mode:    provider.ModeTV,
			src:     provider.MediaSource{URL: "untagged"},
			pref:    allowEnglish,
			wantURL: "untagged",
		},
		{
			name:    "untagged on-demand follows English setting",
			mode:    provider.ModeTV,
			src:     provider.MediaSource{URL: "untagged"},
			pref:    blockEnglish,
			wantURL: "",
		},
		{
			name:    "untagged live always kept",
			mode:    provider.ModeLive,
			src:     provider.MediaSource{URL: "live"},
			pref:    blockEnglish,
			wantURL: "live",
		},
		{
			name:    "live audio label keeps enabled broadcast language",
			mode:    provider.ModeLive,
			src:     provider.MediaSource{URL: "live", AudioLabel: "English · Hindi"},
			pref:    blockEnglish,
			wantURL: "live",
		},
		{
			name:    "anime does not hard filter untagged sources",
			mode:    provider.ModeAnime,
			src:     provider.MediaSource{URL: "anime-untagged"},
			pref:    blockEnglish,
			wantURL: "anime-untagged",
		},
		{
			name:    "anime still respects explicit disable on declared language",
			mode:    provider.ModeAnime,
			src:     provider.MediaSource{URL: "anime-en", Language: "en"},
			pref:    blockEnglish,
			wantURL: "",
		},
		{
			name:    "anime keeps allowed declared language",
			mode:    provider.ModeAnime,
			src:     provider.MediaSource{URL: "anime-hi", Language: "hi"},
			pref:    blockEnglish,
			wantURL: "anime-hi",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := FilterLanguages([]provider.MediaSource{tc.src}, tc.mode, tc.pref)
			if tc.wantURL == "" {
				if len(got) != 0 {
					t.Fatalf("kept %v, want no sources", sourceURLs(got))
				}
				return
			}
			if len(got) != 1 || got[0].URL != tc.wantURL {
				t.Fatalf("kept %v, want %q", sourceURLs(got), tc.wantURL)
			}
		})
	}
}

func TestFilterLanguagesPreservesOrder(t *testing.T) {
	sources := []provider.MediaSource{
		{URL: "one", Language: "en"},
		{URL: "two", Language: "hi"},
		{URL: "three", Language: "en"},
		{URL: "four", Language: "fr"},
	}
	got := sourceURLs(FilterLanguages(sources, provider.ModeMovies, NewLanguagePreference(nil, []string{"English"})))
	if !slices.Equal(got, []string{"two", "four"}) {
		t.Errorf("kept %v, want [two four]", got)
	}
}
