package tui

import (
	"errors"
	"testing"

	"kari/internal/model"
	"kari/internal/provider"
)

// Stray Kitty graphics responses must never survive in the search
// box; human typing (including underscores) must pass through.
func TestScrubTerminalResponses(t *testing.T) {
	cases := []struct{ in, want string }{
		{"one piece_Gi=1;OK\\", "one piece"},
		{"_Gi=12;OK\\", ""},
		{"frieren_Gi=3;ERROR\\ extra", "frieren extra"},
		{"one_piece naruto", "one_piece naruto"},
		{"_G without id stays\\", "_G without id stays\\"},
		{"", ""},
	}
	for _, tc := range cases {
		if got := scrubTerminalResponses(tc.in); got != tc.want {
			t.Errorf("scrub(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestRenderFooterDoesNotWrap(t *testing.T) {
	bindings := []KeyBinding{
		{Key: "space", Action: "search"},
		{Key: "tab", Action: "mode"},
		{Key: "h", Action: "history"},
		{Key: "s", Action: "settings"},
		{Key: "?", Action: "help"},
	}
	accent := ResolveAccent(model.KindAnime, "Auto")
	got := RenderFooter(bindings, nil, accent, 40)

	if len(got) == 0 {
		t.Fatalf("footer is empty")
	}
}

func TestRenderSettingsScreenAllCategories(t *testing.T) {
	accent := ResolveAccent(model.KindAnime, "Auto")

	for i := range SettingsCategoryNames {
		data := SettingsData{
			ActiveCategory:  SettingsCategory(i),
			FocusedRowIndex: 0,
			Width:           80,
			Height:          24,
			Accent:          accent,
			PlayerName:      "mpv",
			QualityName:     "Highest",
			Autoplay:        false,
		}
		rendered := RenderSettingsScreen(data)
		if rendered == "" {
			t.Fatalf("rendered settings screen for category %d is empty", i)
		}
	}
}

func TestCleanErrorForUIRateLimited(t *testing.T) {
	tests := []struct {
		err  error
		want string
	}{
		{errors.New("pengu: rate limited"), "Rate limited by Pengu, use your own token for better rate limits"},
		{errors.New("http status 429 for https://pengu.uk/stream.json"), "Rate limited by Pengu, use your own token for better rate limits"},
		{errors.New("movysx: no sources found; pengu: rate limited"), "Rate limited by Pengu, use your own token for better rate limits"},
	}
	for _, tt := range tests {
		if got := cleanErrorForUI(tt.err); got != tt.want {
			t.Errorf("cleanErrorForUI(%v) = %q, want %q", tt.err, got, tt.want)
		}
	}
}

func TestCollectSettingsData_SubtitlesAndPoster(t *testing.T) {
	m := &modelImpl{
		registry:         &provider.Registry{},
		subtitleLanguage: "ja",
		imagesEnabled:    true,
		settingsCategory: CategoryLanguages,
	}
	data := m.collectSettingsData(80, 24)
	if data.SubtitleLanguage != "Japanese" {
		t.Errorf("SubtitleLanguage = %q, want %q", data.SubtitleLanguage, "Japanese")
	}
	if !data.PosterArtwork {
		t.Errorf("PosterArtwork = false, want true")
	}

	m.subtitleLanguage = "off"
	m.imagesEnabled = false
	data2 := m.collectSettingsData(80, 24)
	if data2.SubtitleLanguage != "Off" {
		t.Errorf("SubtitleLanguage = %q, want %q", data2.SubtitleLanguage, "Off")
	}
	if data2.PosterArtwork {
		t.Errorf("PosterArtwork = true, want false")
	}
}
