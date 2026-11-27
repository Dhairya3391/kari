package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"kari/internal/config"
	"kari/internal/filter"
	"kari/internal/model"
	"kari/internal/provider"
	"kari/internal/service"
)

func TestSubtitlePickerIndexMatchesPreference(t *testing.T) {
	cases := []struct {
		name string
		pref filter.SubtitlePreference
		want int
	}{
		{"any", filter.SubtitlePreference{Mode: filter.SubtitleAny}, 0},
		{"off", filter.SubtitlePreference{Mode: filter.SubtitleOff}, 1},
		{"language", filter.SubtitlePreference{Mode: filter.SubtitlePreferred, Language: "en"}, 2},
	}
	for _, tt := range cases {
		if got := subtitlePickerIndex(tt.pref); got != tt.want {
			t.Errorf("%s: subtitlePickerIndex = %d, want %d", tt.name, got, tt.want)
		}
	}
}

func TestAnimeSettingsShowsSubtitleLanguageRow(t *testing.T) {
	m := newTestSwitchModel()
	m.settingsCategory = CategoryAnime
	rendered := RenderSettingsScreen(m.collectSettingsData(120, 40))
	if !strings.Contains(rendered, "Subtitle language") {
		t.Fatalf("anime settings missing subtitle language row:\n%s", rendered)
	}
}

func TestAnimeSubtitleRowOpensPicker(t *testing.T) {
	m := newTestSwitchModel()
	m.settingsCategory = CategoryAnime
	m.settingsIndex = 2

	mdl, _ := m.updateSettings(tea.KeyMsg{Type: tea.KeyEnter})
	got := mdl.(*modelImpl)
	if !got.subtitlePickerOpen || got.subtitlePickerMode != provider.ModeAnime {
		t.Fatalf("anime subtitle row opened picker=%v mode=%q", got.subtitlePickerOpen, got.subtitlePickerMode)
	}
}

func TestSubtitlePickerSelectionAppliesToAnime(t *testing.T) {
	m := newTestSwitchModel()
	m.openSubtitlePicker(provider.ModeAnime)
	m.subtitlePickerIndex = subtitlePickerIndex(filter.SubtitlePreference{Mode: filter.SubtitlePreferred, Language: "ta"})

	mdl, _ := m.updateSettings(tea.KeyMsg{Type: tea.KeyEnter})
	m = mdl.(*modelImpl)

	pref := m.filterScope.For(provider.ModeAnime).Subtitles
	if pref.Mode != filter.SubtitlePreferred || pref.Language != "ta" {
		t.Fatalf("anime subtitle preference = %+v, want preferred/ta", pref)
	}
	if m.subtitlePickerOpen {
		t.Fatal("picker must close after a selection")
	}
}

func TestSubtitlePickerSyncsDownloadService(t *testing.T) {
	m := newTestSwitchModel()
	m.downloadService = service.NewDownloadService("", nil, nil, nil)
	m.openSubtitlePicker(provider.ModeAnime)
	m.subtitlePickerIndex = subtitlePickerIndex(filter.SubtitlePreference{Mode: filter.SubtitlePreferred, Language: "es"})

	mdl, _ := m.updateSettings(tea.KeyMsg{Type: tea.KeyEnter})
	m = mdl.(*modelImpl)
	if got := m.downloadService.SubtitleLanguage(); got != "es" {
		t.Fatalf("download subtitle language = %q, want es", got)
	}
}

func TestSubtitlePickerAnyClearsLanguage(t *testing.T) {
	m := newTestSwitchModel()
	m.setSubtitleLanguage(provider.ModeAnime, "ta")
	m.openSubtitlePicker(provider.ModeAnime)
	m.subtitlePickerIndex = 0

	mdl, _ := m.updateSettings(tea.KeyMsg{Type: tea.KeyEnter})
	m = mdl.(*modelImpl)

	if pref := m.filterScope.For(provider.ModeAnime).Subtitles; pref.Mode != filter.SubtitleAny {
		t.Fatalf("anime subtitle preference = %+v, want Any", pref)
	}
}

func TestSubtitlePickerSyncsAnimeServiceFlag(t *testing.T) {
	m := newTestSwitchModel()
	m.subtitleService = service.NewSubtitleService(&config.Config{})
	m.setSubtitleLanguage(provider.ModeAnime, "ta")
	if m.subtitleService.AnimeSubtitlesDisabled() {
		t.Fatal("anime subtitles must be enabled after picking a language")
	}

	m.openSubtitlePicker(provider.ModeAnime)
	m.subtitlePickerIndex = subtitlePickerIndex(filter.SubtitlePreference{Mode: filter.SubtitleOff})
	mdl, _ := m.updateSettings(tea.KeyMsg{Type: tea.KeyEnter})
	m = mdl.(*modelImpl)
	if !m.subtitleService.AnimeSubtitlesDisabled() {
		t.Fatal("picking Off must disable anime subtitles in the live service")
	}

	m.openSubtitlePicker(provider.ModeAnime)
	m.subtitlePickerIndex = 0 // Any
	mdl, _ = m.updateSettings(tea.KeyMsg{Type: tea.KeyEnter})
	m = mdl.(*modelImpl)
	if m.subtitleService.AnimeSubtitlesDisabled() {
		t.Fatal("picking Any must re-enable anime subtitles in the live service")
	}
}

func TestSourcesWithSubtitleHidesHardSubsWhenLanguageAvailable(t *testing.T) {
	m := newTestSwitchModel()
	m.appMode = provider.ModeAnime
	hard := provider.MediaSource{URL: "hard", SubType: provider.SubTypeHard}
	soft := provider.MediaSource{
		URL: "soft", SubType: provider.SubTypeSoft,
		Subtitles: []provider.SubtitleOption{{URL: "es.vtt", Language: "es"}},
	}
	m.resolved = &model.ResolvedMedia{Playback: []provider.MediaSource{hard, soft}}
	m.setSubtitleLanguage(provider.ModeAnime, "es")

	m.rankAndSelectSources()
	if len(m.rankedSources) != 1 || m.rankedSources[0].Source.URL != "soft" {
		t.Fatalf("ranked sources = %+v, want only the Spanish source", m.rankedSources)
	}
}

func TestSourcesWithSubtitleKeepsAllSourcesWhenLanguageUnavailable(t *testing.T) {
	m := newTestSwitchModel()
	m.appMode = provider.ModeAnime
	hard := provider.MediaSource{URL: "hard", SubType: provider.SubTypeHard}
	soft := provider.MediaSource{
		URL: "soft", SubType: provider.SubTypeSoft,
		Subtitles: []provider.SubtitleOption{{URL: "en.vtt", Language: "en"}},
	}
	m.resolved = &model.ResolvedMedia{Playback: []provider.MediaSource{hard, soft}}
	m.setSubtitleLanguage(provider.ModeAnime, "id")

	m.rankAndSelectSources()
	if len(m.rankedSources) != 2 {
		t.Fatalf("ranked sources = %d, want both when no source has Indonesian", len(m.rankedSources))
	}
}

func TestPreviewHardSubsDoNotClaimPreferenceLanguage(t *testing.T) {
	m := newTestSwitchModel()
	m.appMode = provider.ModeAnime
	src := provider.MediaSource{URL: "hard", SubType: provider.SubTypeHard}
	m.resolved = &model.ResolvedMedia{Playback: []provider.MediaSource{src}}
	m.rankedSources = []filter.RankedSource{{Source: src}}
	m.setSubtitleLanguage(provider.ModeAnime, "es")
	m.subtitleLangUsed = ""

	data := m.collectPreviewData(100, 24, lipgloss.AdaptiveColor{})
	if data.SubtitlesText != "hard subs" || data.SubtitleType != "" {
		t.Fatalf("subtitle display = %q / %q, want 'hard subs' with no language claim", data.SubtitlesText, data.SubtitleType)
	}
}

func TestPreferredAudioForUsesUserOrder(t *testing.T) {
	src := provider.MediaSource{Audio: provider.AudioInfo{Languages: []string{"ja", "en", "es"}}}
	if got := preferredAudioFor(src, []string{"es", "en"}); got != "es" {
		t.Errorf("preferredAudioFor = %q, want es", got)
	}
	if got := preferredAudioFor(src, []string{"hi"}); got != "" {
		t.Errorf("preferredAudioFor absent language = %q, want empty", got)
	}

	tracksOnly := provider.MediaSource{AudioTracks: []provider.AudioTrackRef{{Code: "tam"}, {Code: "tel"}}}
	if got := preferredAudioFor(tracksOnly, []string{"te"}); got != "te" {
		t.Errorf("preferredAudioFor track code = %q, want te", got)
	}
}
