package tui

import (
	"errors"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"

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

func TestFormatKeyLabelUsesNativeModifierNames(t *testing.T) {
	if got := formatKeyLabelForOS("alt+backspace / ctrl+w", "darwin"); got != "Option+Backspace / Control+w" {
		t.Fatalf("macOS key label = %q, want Option+Backspace / Control+w", got)
	}
	if got := formatKeyLabelForOS("alt+backspace / ctrl+w", "linux"); got != "Alt+Backspace / Ctrl+w" {
		t.Fatalf("Linux key label = %q, want Alt+Backspace / Ctrl+w", got)
	}
	if got := formatKeyLabelForOS("tab/⇧tab", "darwin"); got != "Tab / Shift+Tab" {
		t.Fatalf("Tab key label = %q, want Tab / Shift+Tab", got)
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

func TestRenderSettingsScreenDoesNotRepeatCategoryHeading(t *testing.T) {
	data := SettingsData{
		ActiveCategory:  CategoryPlayback,
		FocusedRowIndex: 0,
		Width:           100,
		Accent:          ResolveAccent(model.KindAnime, "Auto"),
		PlayerName:      "mpv",
		QualityName:     "Highest",
	}
	if got := strings.Count(RenderSettingsScreen(data), "Playback"); got != 1 {
		t.Fatalf("Playback appears %d times, want only the active tab label", got)
	}
}

func TestRenderSettingsScreenFitsNarrowTerminal(t *testing.T) {
	for category := range SettingsCategoryNames {
		data := SettingsData{
			ActiveCategory:   SettingsCategory(category),
			FocusedRowIndex:  0,
			Width:            28,
			Height:           12,
			Accent:           ResolveAccent(model.KindAnime, "Auto"),
			PlayerName:       "mpv",
			QualityName:      "Highest",
			AudioSummary:     "English · Hindi · Japanese",
			SubtitleLanguage: "English",
			AnimeAudioTrack:  "Dub · English",
			DefaultModeName:  "Last active",
			AccentName:       "Auto (per mode)",
			ModesRows:        []ModeRow{{Label: "Jellyfin server", Available: false, UnavailNote: "configure server"}},
			ProviderRows:     []ProviderRow{{Section: "Anime", Name: "A long provider display name", Available: true, Enabled: true}},
		}
		for lineNo, line := range strings.Split(RenderSettingsScreen(data), "\n") {
			if width := lipgloss.Width(line); width > data.Width {
				t.Errorf("category %d line %d width = %d, exceeds %d", category, lineNo+1, width, data.Width)
			}
		}
	}
}

func TestCleanErrorForUIRateLimited(t *testing.T) {
	tests := []struct {
		err  error
		want string
	}{
		{errors.New("provider-a: rate limited"), "A provider is rate limited — please try again"},
		{errors.New("http status 429 for https://provider.example/stream.json"), "A provider is rate limited — please try again"},
		{errors.New("provider-b: no sources found; provider-a: rate limited"), "A provider is rate limited — please try again"},
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
