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
	m.startupSearchActive = false
	m.disabledProviders = map[string]bool{"pengu": true, "anivault": true}
	m.autoUpdate = true

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
	if saved.StartupSearchActive == nil || *saved.StartupSearchActive {
		t.Errorf("saved.StartupSearchActive = %v, want false", saved.StartupSearchActive)
	}
	if !saved.AutoUpdate {
		t.Errorf("saved.AutoUpdate = false, want true")
	}
	wantDisabledProviders := []string{"anivault", "pengu"}
	if !reflect.DeepEqual(saved.DisabledProviders, wantDisabledProviders) {
		t.Errorf("saved.DisabledProviders = %v, want %v", saved.DisabledProviders, wantDisabledProviders)
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
	startupSearchActiveFalse := false
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
		StartupSearchActive:   &startupSearchActiveFalse,
	})

	reg := &provider.Registry{}
	reg.Register(&fakeProviderForSwitch{name: "anikoto", modes: []provider.Mode{{Name: provider.ModeAnime}}})
	reg.Register(&fakeProviderForSwitch{name: "tv_p", modes: []provider.Mode{{Name: provider.ModeTV}}})
	reg.Register(&fakeProviderForSwitch{name: "movie_p", modes: []provider.Mode{{Name: provider.ModeMovies}}})

	players := player.NewRegistry("", player.SkipClients{}, player.SkipSettings{})
	mdl := NewModel(context.Background(), "", reg, players, "", nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, "1.0.0", "")
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
	if m.startupSearchActive {
		t.Errorf("m.startupSearchActive = true, want false")
	}
	if m.queryInput.Focused() {
		t.Errorf("m.queryInput.Focused() = true, want false")
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
	restartedMdl := NewModel(context.Background(), "", reg, players, "", nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, "1.0.0", "")
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

func TestStartupSearchActivePersistence(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	m := newTestSwitchModel()
	m.settingsCategory = CategoryInterface
	m.settingsIndex = 3 // Startup search active
	m.startupSearchActive = true

	// Toggle via enter key
	m.updateSettings(tea.KeyMsg{Type: tea.KeyEnter})
	if m.startupSearchActive {
		t.Fatalf("expected m.startupSearchActive to be false after enter")
	}

	saved := settings.Load()
	if saved == nil || saved.StartupSearchActive == nil || *saved.StartupSearchActive {
		t.Fatalf("expected saved.StartupSearchActive to be false, got %+v", saved)
	}

	// Toggle back via right arrow
	m.updateSettings(tea.KeyMsg{Type: tea.KeyRight})
	if !m.startupSearchActive {
		t.Fatalf("expected m.startupSearchActive to be true after right arrow")
	}

	saved = settings.Load()
	if saved == nil || saved.StartupSearchActive == nil || !*saved.StartupSearchActive {
		t.Fatalf("expected saved.StartupSearchActive to be true, got %+v", saved)
	}
}

func TestStartupSearchActiveNewModel(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	f := false
	settings.Save(&settings.Data{
		StartupSearchActive: &f,
	})

	reg := &provider.Registry{}
	players := player.NewRegistry("", player.SkipClients{}, player.SkipSettings{})

	// 1. With startup_search_active: false and empty initialQuery
	mdl := NewModel(context.Background(), "", reg, players, "", nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, "1.0.0", "")
	m, ok := mdl.(*modelImpl)
	if !ok {
		t.Fatal("expected *modelImpl")
	}
	if m.startupSearchActive {
		t.Error("expected m.startupSearchActive to be false")
	}
	if m.queryInput.Focused() {
		t.Error("expected queryInput to be blurred when startupSearchActive is false")
	}

	// Pressing 'h' in search home view immediately executes history refresh without esc
	mdl2, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'h'}})
	m2 := mdl2.(*modelImpl)
	if !m2.loading || m2.loadingText != "Loading history..." {
		t.Errorf("expected loading history without Esc, got loading=%v, loadingText=%q", m2.loading, m2.loadingText)
	}

	// Pressing space when blurred focuses the search input
	mdlSpace, _ := m.Update(tea.KeyMsg{Type: tea.KeySpace})
	mSpace := mdlSpace.(*modelImpl)
	if !mSpace.queryInput.Focused() {
		t.Error("expected space key to focus search input")
	}

	// 2. With startup_search_active: false but non-empty initialQuery ("frieren")
	mdlQuery := NewModel(context.Background(), "frieren", reg, players, "", nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, "1.0.0", "")
	mQuery := mdlQuery.(*modelImpl)
	if !mQuery.queryInput.Focused() {
		t.Error("expected queryInput to be focused when initialQuery is provided")
	}
	if mQuery.searchQuery != "frieren" {
		t.Errorf("expected searchQuery to be 'frieren', got %q", mQuery.searchQuery)
	}

	// 3. With startup_search_active: true (or absent)
	tr := true
	settings.Save(&settings.Data{
		StartupSearchActive: &tr,
	})
	mdlActive := NewModel(context.Background(), "", reg, players, "", nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, "1.0.0", "")
	mActive := mdlActive.(*modelImpl)
	if !mActive.startupSearchActive {
		t.Error("expected m.startupSearchActive to be true")
	}
	if !mActive.queryInput.Focused() {
		t.Error("expected queryInput to be focused when startupSearchActive is true")
	}
}

func TestProviderToggleInSettingsPersists(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	reg := &provider.Registry{}
	reg.Register(&emptyQueryStubProvider{name: "first_prov", modes: []provider.Mode{{Name: provider.ModeAnime, Priority: 1}}})
	reg.Register(&emptyQueryStubProvider{name: "second_prov", modes: []provider.Mode{{Name: provider.ModeAnime, Priority: 1}}})

	m := newTestSwitchModel()
	m.registry = reg
	m.disabledProviders = make(map[string]bool)

	// Toggle first provider off
	m.toggleProviderAtIndex(0)
	if !m.isProviderDisabledKey(provider.ModeAnime, "first_prov") {
		t.Fatal("expected first_prov to be disabled for anime")
	}

	saved := settings.Load()
	if saved == nil || len(saved.DisabledProviders) != 1 || saved.DisabledProviders[0] != "anime:first_prov" {
		t.Fatalf("saved.DisabledProviders = %v, want ['anime:first_prov']", saved.DisabledProviders)
	}

	// Toggle first provider back on
	m.toggleProviderAtIndex(0)
	if m.isProviderDisabledKey(provider.ModeAnime, "first_prov") {
		t.Fatal("expected first_prov to be enabled for anime")
	}

	saved2 := settings.Load()
	if saved2 == nil || len(saved2.DisabledProviders) != 0 {
		t.Fatalf("saved2.DisabledProviders = %v, want empty", saved2.DisabledProviders)
	}
}
