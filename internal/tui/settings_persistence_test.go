package tui

import (
	"context"
	"errors"
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
	m.preferredProviders = map[string]string{"movies": "vyla", "anime": "anikoto"}
	m.autoplay = true
	m.startupSync = true
	m.defaultMode = "movies"
	m.transitions = false
	m.startupSearchActive = false
	m.disabledProviders = map[string]bool{"pengu": true, "anivault": true}
	m.autoUpdate = true
	m.disableDownloadVerification = true

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
	if !reflect.DeepEqual(saved.PreferredProviders, m.preferredProviders) {
		t.Errorf("saved.PreferredProviders = %v, want %v", saved.PreferredProviders, m.preferredProviders)
	}
	if !saved.Autoplay {
		t.Errorf("saved.Autoplay = false, want true")
	}
	if !saved.DisableDownloadVerification {
		t.Errorf("saved.DisableDownloadVerification = false, want true")
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

func TestAddonManifestIsSavedOnlyAfterSuccessfulCheck(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	m := newTestSwitchModel()
	m.appCtx = context.Background()
	m.settingsCategory = CategoryAddons
	m.addonInputOpen = true
	m.addonInput.SetValue("https://addon.example/manifest.json")
	m.addonManifestTester = func(context.Context, string) (provider.Provider, error) {
		return nil, errors.New("no playable streams")
	}
	_, cmd := m.updateSettings(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil || !m.addonTesting || len(m.stremioAddonManifests) != 0 {
		t.Fatal("addon should be pending verification and not saved")
	}
	result := cmd()
	m.updateSettings(result)
	if m.addonTesting || len(m.stremioAddonManifests) != 0 {
		t.Fatal("failed addon check should leave the addon unsaved")
	}
	if saved := settings.Load(); saved != nil && len(saved.StremioAddonManifests) > 0 {
		t.Fatalf("failed addon check persisted manifests: %v", saved.StremioAddonManifests)
	}

	m.addonManifestTester = func(context.Context, string) (provider.Provider, error) {
		return &fakeProviderForSwitch{name: "verified-addon", modes: []provider.Mode{{Name: provider.ModeMovies}}}, nil
	}
	m.addonInputOpen = true
	m.addonInput.SetValue("https://addon.example/manifest.json")
	_, cmd = m.updateSettings(tea.KeyMsg{Type: tea.KeyEnter})
	result = cmd()
	m.updateSettings(result)
	saved := settings.Load()
	if saved == nil || len(saved.StremioAddonManifests) != 1 || saved.StremioAddonManifests[0] != "https://addon.example/manifest.json" {
		t.Fatalf("successful addon check saved manifests = %v", saved)
	}
}

type configuredFakeForRemoval struct {
	fakeProviderForSwitch
	manifestURL string
}

func (f *configuredFakeForRemoval) MatchesConfiguration(value string) bool {
	return value == f.manifestURL
}

func TestRemovingAddonUnregistersItImmediately(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	const manifestURL = "https://addon.example/manifest.json?token=secret"
	m := newTestSwitchModel()
	m.registry.Register(&configuredFakeForRemoval{
		fakeProviderForSwitch: fakeProviderForSwitch{name: "addon-test", modes: []provider.Mode{{Name: provider.ModeStremio}}},
		manifestURL:           manifestURL,
	})
	m.configuredModes = append(m.configuredModes, string(provider.ModeStremio))
	m.updateEffectiveModes()
	m.appMode = provider.ModeStremio
	m.stremioAddonManifests = []string{manifestURL}
	m.settingsCategory = CategoryAddons
	m.settingsIndex = 1
	m.disabledProviders = map[string]bool{"addon-test": true, "tv:addon-test": true}

	m.updateSettings(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'d'}})

	if len(m.stremioAddonManifests) != 0 {
		t.Fatalf("configured addons = %v, want empty", m.stremioAddonManifests)
	}
	if m.appMode == provider.ModeStremio {
		t.Fatal("removed addon's mode stayed active")
	}
	if _, removed := m.registry.UnregisterConfigured(manifestURL); removed {
		t.Fatal("addon remained registered after removal")
	}
	if len(m.disabledProviders) != 0 {
		t.Fatalf("removed provider's disabled settings remained: %v", m.disabledProviders)
	}
}

func TestSettings_VerifyDownloadsToggle(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	m := newTestSwitchModel()
	m.settingsCategory = CategoryPlayback
	m.settingsIndex = 5

	if m.disableDownloadVerification {
		t.Fatalf("expected disableDownloadVerification to be false initially")
	}

	m.cycleCurrentSetting(1)
	if !m.disableDownloadVerification {
		t.Fatalf("expected disableDownloadVerification to be true after cycling")
	}

	data := m.collectSettingsData(80, 24)
	if data.VerifyDownloads {
		t.Fatalf("expected SettingsData.VerifyDownloads to be false when disabled")
	}

	m.cycleCurrentSetting(-1)
	if m.disableDownloadVerification {
		t.Fatalf("expected disableDownloadVerification to be false after cycling back")
	}

	data2 := m.collectSettingsData(80, 24)
	if !data2.VerifyDownloads {
		t.Fatalf("expected SettingsData.VerifyDownloads to be true when enabled")
	}
}

func TestProviderPriorityCanBeSetForEachMode(t *testing.T) {
	m := newTestSwitchModel()
	m.settingsCategory = CategoryPlayback
	m.openProviderPriorityEditor()
	for modes := m.providerPriorityModes(); modes[m.providerPriorityIndex] != provider.ModeMovies; {
		m.updateProviderPriorityPicker("down")
	}
	if m.providerPriorityModes()[m.providerPriorityIndex] != provider.ModeMovies {
		t.Fatalf("provider mode = %q, want movies", m.providerPriorityModes()[m.providerPriorityIndex])
	}
	m.cycleProviderPriority(provider.ModeMovies, 1)
	if got := m.preferredProviderForMode(provider.ModeMovies); got != "movie_p" {
		t.Fatalf("movies preferred provider = %q, want movie_p", got)
	}
	if got := m.preferredProviderForMode(provider.ModeAnime); got != "" {
		t.Fatalf("anime preferred provider changed unexpectedly: %q", got)
	}
}

func TestProviderPriorityEscapeReturnsToSettings(t *testing.T) {
	m := newTestSwitchModel()
	m.activeView = viewSettings
	m.openProviderPriorityEditor()
	_, handled := m.handleGlobalKeys(tea.KeyMsg{Type: tea.KeyEsc})
	if !handled || m.providerPriorityOpen || m.activeView != viewSettings {
		t.Fatalf("Escape state: handled=%v picker=%v view=%q", handled, m.providerPriorityOpen, m.activeView)
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
		PreferredProviders:    map[string]string{"tv": "tv_p", "movies": "movie_p"},
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
	mdl := NewModel(context.Background(), "", reg, players, "", nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, "1.0.0", "")
	m, ok := mdl.(*modelImpl)
	if !ok {
		t.Fatal("expected *modelImpl")
	}

	wantConfigured := []string{"tv", "movies", "anime", "cartoon", "live", "manga", "jellyfin", "stremio"}
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
	m.appMode = provider.ModeTV
	if got := m.preferredProviderForMode(provider.ModeTV); got != "tv_p" {
		t.Errorf("TV preferred provider = %q, want tv_p", got)
	}
	m.appMode = provider.ModeMovies
	if got := m.preferredProviderForMode(provider.ModeMovies); got != "movie_p" {
		t.Errorf("movie preferred provider = %q, want movie_p", got)
	}
	m.appMode = provider.ModeTV
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
	// Initially: anime, cartoon, live, manga, movies, tv, jellyfin, Stremio.
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
	restartedMdl := NewModel(context.Background(), "", reg, players, "", nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, "1.0.0", "")
	restarted, ok := restartedMdl.(*modelImpl)
	if !ok {
		t.Fatal("expected *modelImpl")
	}

	restartedWant := append(append([]string(nil), wantOrder...), "stremio")
	if !reflect.DeepEqual(restarted.configuredModes, restartedWant) {
		t.Errorf("after restart restarted.configuredModes = %v, want %v", restarted.configuredModes, restartedWant)
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
	mdl := NewModel(context.Background(), "", reg, players, "", nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, "1.0.0", "")
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
	mdlQuery := NewModel(context.Background(), "frieren", reg, players, "", nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, "1.0.0", "")
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
	mdlActive := NewModel(context.Background(), "", reg, players, "", nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, "1.0.0", "")
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
