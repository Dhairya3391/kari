package tui

import (
	"context"
	"reflect"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"kari/internal/model"
	"kari/internal/player"
	"kari/internal/provider"
	"kari/internal/settings"
)

func TestSaveSettingsPersistsModesAndPreferences(t *testing.T) {
	tempHome := t.TempDir()
	t.Setenv("HOME", tempHome)

	m := newTestSwitchModel()
	m.configuredModes = []string{"movies", "tv", "anime", "cartoon", "live", "manga", "jellyfin"}
	m.imagesEnabled = false
	m.disableAnimeSubtitles = true
	m.subtitleLanguage = "ja"
	m.qualityMode = qualityHighest
	m.downloadQuality = downloadQualityFHD
	m.autoplay = true
	m.startupSync = true
	m.defaultMode = "movies"
	m.transitions = false

	m.saveSettings()

	saved := settings.Load()
	if saved == nil {
		t.Fatal("expected settings.Load() to return saved data")
	}

	wantModes := []string{"movies", "tv", "anime", "cartoon", "live", "manga", "jellyfin"}
	if !reflect.DeepEqual(saved.Modes, wantModes) {
		t.Errorf("saved.Modes = %v, want %v", saved.Modes, wantModes)
	}
	if !saved.DisableImages {
		t.Errorf("saved.DisableImages = false, want true")
	}
	if !saved.DisableAnimeSubtitles {
		t.Errorf("saved.DisableAnimeSubtitles = false, want true")
	}
	if saved.SubtitleLanguage != "ja" {
		t.Errorf("saved.SubtitleLanguage = %q, want 'ja'", saved.SubtitleLanguage)
	}
	if saved.QualityMode != qualityHighest {
		t.Errorf("saved.QualityMode = %d, want %d", saved.QualityMode, qualityHighest)
	}
	if saved.DownloadQuality != downloadQualityFHD {
		t.Errorf("saved.DownloadQuality = %d, want %d", saved.DownloadQuality, downloadQualityFHD)
	}
	if !saved.Autoplay {
		t.Errorf("saved.Autoplay = false, want true")
	}
	if !saved.StartupSync {
		t.Errorf("saved.StartupSync = false, want true")
	}
	if saved.DefaultMode != "movies" {
		t.Errorf("saved.DefaultMode = %q, want 'movies'", saved.DefaultMode)
	}
	if saved.Transitions == nil || *saved.Transitions {
		t.Errorf("saved.Transitions = %v, want false", saved.Transitions)
	}
}

func TestGlobalPlayerSwitchPersistsAcrossRestart(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	players := player.NewRegistry("", player.SkipClients{}, player.SkipSettings{})
	players.Register(&availableTestPlayer{name: "first-test-player"})
	players.Register(&availableTestPlayer{name: "second-test-player"})
	m := &modelImpl{
		players:          players,
		availablePlayers: players.AvailablePlayers(),
		languageFilter:   make(map[string]bool),
		configuredModes:  (&settings.Data{}).NormalizedModes(),
		transitions:      true,
	}
	m.selectedPlayer = 0

	if _, handled := m.handleGlobalKeys(tea.KeyMsg{Type: tea.KeyCtrlP}); !handled {
		t.Fatal("Ctrl+P was not handled")
	}

	saved := settings.Load()
	if saved == nil {
		t.Fatal("player selection was not saved")
	}
	if saved.PreferredPlayer != m.selectedPlayerName() {
		t.Fatalf("saved preferred player = %q, want %q", saved.PreferredPlayer, m.selectedPlayerName())
	}
}

type availableTestPlayer struct {
	name string
}

func (p *availableTestPlayer) Name() string { return p.name }

func (p *availableTestPlayer) Available() bool { return true }

func (p *availableTestPlayer) Play([]provider.MediaSource, model.ResolvedMedia) (player.PlaybackResult, error) {
	return player.PlaybackResult{}, nil
}

func TestNewModelRestoresSettingsAfterRestart(t *testing.T) {
	tempHome := t.TempDir()
	t.Setenv("HOME", tempHome)

	transitionsFalse := false
	settings.Save(&settings.Data{
		Modes:                 []string{"tv", "movies", "anime", "cartoon", "live", "manga", "jellyfin"},
		DisableImages:         true,
		DisableAnimeSubtitles: true,
		SubtitleLanguage:      "ja",
		QualityMode:           qualityLowest,
		DownloadQuality:       downloadQuality4K,
		Autoplay:              true,
		StartupSync:           true,
		DefaultMode:           "tv",
		Transitions:           &transitionsFalse,
	})

	reg := &provider.Registry{}
	reg.Register(&fakeProviderForSwitch{name: "anikoto", modes: []provider.Mode{{Name: provider.ModeAnime}}})
	reg.Register(&fakeProviderForSwitch{name: "tv_p", modes: []provider.Mode{{Name: provider.ModeTV}}})
	reg.Register(&fakeProviderForSwitch{name: "movie_p", modes: []provider.Mode{{Name: provider.ModeMovies}}})

	players := player.NewRegistry("", player.SkipClients{}, player.SkipSettings{})
	mdl := NewModel(context.Background(), "", reg, players, "", nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, "1.0.0", "")
	m, ok := mdl.(*modelImpl)
	if !ok {
		t.Fatal("expected *modelImpl")
	}

	wantConfigured := []string{"tv", "movies", "anime", "cartoon", "live", "manga", "jellyfin"}
	if !reflect.DeepEqual(m.configuredModes, wantConfigured) {
		t.Errorf("m.configuredModes = %v, want %v", m.configuredModes, wantConfigured)
	}
	if m.imagesEnabled {
		t.Errorf("m.imagesEnabled = true, want false (DisableImages was true)")
	}
	if !m.disableAnimeSubtitles {
		t.Errorf("m.disableAnimeSubtitles = false, want true")
	}
	if m.subtitleLanguage != "ja" {
		t.Errorf("m.subtitleLanguage = %q, want 'ja'", m.subtitleLanguage)
	}
	if m.qualityMode != qualityLowest {
		t.Errorf("m.qualityMode = %d, want %d", m.qualityMode, qualityLowest)
	}
	if m.downloadQuality != downloadQuality4K {
		t.Errorf("m.downloadQuality = %d, want %d", m.downloadQuality, downloadQuality4K)
	}
	if !m.autoplay {
		t.Errorf("m.autoplay = false, want true")
	}
	if !m.startupSync {
		t.Errorf("m.startupSync = false, want true")
	}
	if m.transitions {
		t.Errorf("m.transitions = true, want false")
	}
	if m.appMode != provider.ModeTV {
		t.Errorf("m.appMode = %v, want %v", m.appMode, provider.ModeTV)
	}
}

func TestModeReorderPersistsAcrossRestart(t *testing.T) {
	tempHome := t.TempDir()
	t.Setenv("HOME", tempHome)

	m := newTestSwitchModel()
	// Focus on second mode (index 2 in Settings > Modes, where index 0 is Startup mode)
	// Initially: anime, cartoon, live, manga, movies, tv, jellyfin
	m.settingsIndex = 2 // cartoon (idx = 1)
	m.reorderMode(-1)   // move cartoon before anime -> cartoon, anime, live...

	wantOrder := []string{"cartoon", "anime", "live", "manga", "movies", "tv", "jellyfin"}
	if !reflect.DeepEqual(m.configuredModes, wantOrder) {
		t.Fatalf("after reorder m.configuredModes = %v, want %v", m.configuredModes, wantOrder)
	}

	// Verify file was written
	saved := settings.Load()
	if saved == nil {
		t.Fatal("expected settings file to exist")
	}
	if !reflect.DeepEqual(saved.Modes, wantOrder) {
		t.Fatalf("saved.Modes = %v, want %v", saved.Modes, wantOrder)
	}

	// Simulate restart
	reg := &provider.Registry{}
	reg.Register(&fakeProviderForSwitch{name: "anikoto", modes: []provider.Mode{{Name: provider.ModeAnime}}})
	reg.Register(&fakeProviderForSwitch{name: "cartoon_p", modes: []provider.Mode{{Name: provider.ModeCartoon}}})

	players := player.NewRegistry("", player.SkipClients{}, player.SkipSettings{})
	restartedMdl := NewModel(context.Background(), "", reg, players, "", nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, "1.0.0", "")
	restarted, ok := restartedMdl.(*modelImpl)
	if !ok {
		t.Fatal("expected *modelImpl")
	}

	if !reflect.DeepEqual(restarted.configuredModes, wantOrder) {
		t.Errorf("after restart restarted.configuredModes = %v, want %v", restarted.configuredModes, wantOrder)
	}
}

func TestCycleSkipProvider(t *testing.T) {
	expected := []string{"hybrid", "skipdb", "introdb", "anime-skip", "aniskip", "off"}
	cur := "hybrid"
	for _, want := range expected[1:] {
		cur = cycleSkipProvider(cur, false)
		if cur != want {
			t.Fatalf("cycle forward want %s, got %s", want, cur)
		}
	}
	cur = cycleSkipProvider(cur, false)
	if cur != "hybrid" {
		t.Fatalf("cycle wrap forward want hybrid, got %s", cur)
	}
	cur = cycleSkipProvider("hybrid", true)
	if cur != "off" {
		t.Fatalf("cycle wrap reverse want off, got %s", cur)
	}
}
