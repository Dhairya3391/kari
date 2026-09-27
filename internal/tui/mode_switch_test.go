package tui

import (
	"context"
	"reflect"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"kari/internal/provider"
)

func newTestSwitchModel() *modelImpl {
	m := &modelImpl{
		keys:            defaultKeyMap(),
		appMode:         provider.ModeAnime,
		configuredModes: []string{"anime", "cartoon", "live", "manga", "movies", "tv", "jellyfin"},
		disabledModes:   make(map[string]bool),
		registry:        &provider.Registry{},
		transitions:     true,
	}
	// Register dummy providers for all modes so they are available
	m.registry.Register(&fakeProviderForSwitch{name: "anikoto", modes: []provider.Mode{{Name: provider.ModeAnime}}})
	m.registry.Register(&fakeProviderForSwitch{name: "cartoon_p", modes: []provider.Mode{{Name: provider.ModeCartoon}}})
	m.registry.Register(&fakeProviderForSwitch{name: "live_p", modes: []provider.Mode{{Name: provider.ModeLive}}})
	m.registry.Register(&fakeProviderForSwitch{name: "manga_p", modes: []provider.Mode{{Name: provider.ModeManga}}})
	m.registry.Register(&fakeProviderForSwitch{name: "movie_p", modes: []provider.Mode{{Name: provider.ModeMovies}}})
	m.registry.Register(&fakeProviderForSwitch{name: "tv_p", modes: []provider.Mode{{Name: provider.ModeTV}}})
	m.registry.Register(&fakeProviderForSwitch{name: "jf_p", modes: []provider.Mode{{Name: provider.ModeJellyfin}}})

	m.updateEffectiveModes()
	return m
}

type fakeProviderForSwitch struct {
	name  string
	modes []provider.Mode
}

func (f *fakeProviderForSwitch) Name() string           { return f.name }
func (f *fakeProviderForSwitch) Modes() []provider.Mode { return f.modes }
func (f *fakeProviderForSwitch) Features(provider.ContentType) provider.Features {
	return provider.Features{}
}
func (f *fakeProviderForSwitch) Search(context.Context, string, provider.ContentType) ([]provider.SearchResult, error) {
	return nil, nil
}
func (f *fakeProviderForSwitch) FetchEpisodes(context.Context, provider.SearchResult) ([]provider.Episode, error) {
	return nil, nil
}
func (f *fakeProviderForSwitch) ResolveSource(context.Context, string, provider.Episode) ([]provider.MediaSource, error) {
	return nil, nil
}
func TestModeCyclingWrapAndEffective(t *testing.T) {
	m := newTestSwitchModel()
	if len(m.modes) != 7 {
		t.Fatalf("modes count = %d, want 7", len(m.modes))
	}

	// Forward cycle: anime -> cartoon
	m.cycleMode(false)
	if m.appMode != provider.ModeCartoon {
		t.Errorf("appMode = %s, want cartoon", m.appMode)
	}

	// Reverse cycle: cartoon -> anime -> jellyfin
	m.cycleMode(true)
	if m.appMode != provider.ModeAnime {
		t.Errorf("appMode = %s, want anime", m.appMode)
	}
	m.cycleMode(true)
	if m.appMode != provider.ModeJellyfin {
		t.Errorf("appMode = %s, want jellyfin (wrapped)", m.appMode)
	}
}

func TestModeOneEffectiveModeNoop(t *testing.T) {
	m := newTestSwitchModel()
	// Disable all except anime
	for _, k := range m.configuredModes {
		if k != "anime" {
			m.disabledModes[k] = true
		}
	}
	m.updateEffectiveModes()

	if len(m.modes) != 1 {
		t.Fatalf("modes count = %d, want 1", len(m.modes))
	}

	cmd := m.cycleMode(false)
	if cmd != nil {
		t.Errorf("expected nil cmd for 1 mode, got %v", cmd)
	}
	if m.appMode != provider.ModeAnime {
		t.Errorf("appMode = %s, want anime", m.appMode)
	}
}

func TestDigitJumpInControlMode(t *testing.T) {
	m := newTestSwitchModel()
	m.activeView = viewSearch

	// Key "2" jumps to cartoon (2nd mode in default list)
	mdl, _ := m.updateSearch(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("2")})
	m = mdl.(*modelImpl)
	if m.appMode != provider.ModeCartoon {
		t.Errorf("appMode = %s, want cartoon", m.appMode)
	}

	// Key "4" jumps to manga (4th mode in default list)
	mdl, _ = m.updateSearch(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("4")})
	m = mdl.(*modelImpl)
	if m.appMode != provider.ModeManga {
		t.Errorf("appMode = %s, want manga", m.appMode)
	}
}
func TestDigitJumpFollowsCustomReorderedModes(t *testing.T) {
	m := newTestSwitchModel()
	m.activeView = viewSearch

	// Custom reorder: movies (1), tv (2), anime (3)
	m.configuredModes = []string{"movies", "tv", "anime", "cartoon", "live", "manga", "jellyfin"}
	m.updateEffectiveModes()

	// Digit "1" should now jump to movies
	mdl, _ := m.updateSearch(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("1")})
	m = mdl.(*modelImpl)
	if m.appMode != provider.ModeMovies {
		t.Errorf("digit 1 = %s, want movies", m.appMode)
	}

	// Digit "2" should now jump to tv
	mdl, _ = m.updateSearch(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("2")})
	m = mdl.(*modelImpl)
	if m.appMode != provider.ModeTV {
		t.Errorf("digit 2 = %s, want tv", m.appMode)
	}

	// Digit "3" should now jump to anime
	mdl, _ = m.updateSearch(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("3")})
	m = mdl.(*modelImpl)
	if m.appMode != provider.ModeAnime {
		t.Errorf("digit 3 = %s, want anime", m.appMode)
	}
}

func TestModeSettingsReorderAndToggle(t *testing.T) {
	m := newTestSwitchModel()
	m.activeView = viewSettings
	m.settingsCategory = CategoryModes
	m.settingsIndex = 1 // mode row 0: "anime" (row 0 is "Startup mode")

	// Move anime down (index 0 -> index 1 in configuredModes)
	m.reorderMode(1)
	if m.settingsIndex != 2 {
		t.Errorf("settingsIndex = %d, want 2", m.settingsIndex)
	}
	if m.configuredModes[0] != "cartoon" || m.configuredModes[1] != "anime" {
		t.Errorf("configuredModes = %v, want cartoon, anime, ...", m.configuredModes)
	}

	// Toggle cartoon (index 0) off
	m.settingsIndex = 1
	m.toggleModeAtIndex(0)
	if !m.disabledModes["cartoon"] {
		t.Error("expected cartoon to be disabled")
	}
	if reflect.DeepEqual(m.modes, []provider.ContentType{provider.ModeAnime, provider.ModeLive, provider.ModeManga, provider.ModeMovies, provider.ModeTV, provider.ModeJellyfin}) == false {
		t.Errorf("effective modes = %v", m.modes)
	}
}

func TestModeSettingsDefaultStartupModeCycle(t *testing.T) {
	m := newTestSwitchModel()
	m.activeView = viewSettings
	m.settingsCategory = CategoryModes
	m.settingsIndex = 0 // "Startup mode"

	// Initial defaultMode is "" (treated as "last")
	m.cycleDefaultMode(1) // next after "last" is "first"
	if m.defaultMode != "first" {
		t.Errorf("defaultMode = %q, want 'first'", m.defaultMode)
	}

	m.cycleDefaultMode(1) // next after "first" is "anime"
	if m.defaultMode != "anime" {
		t.Errorf("defaultMode = %q, want 'anime'", m.defaultMode)
	}
}

func TestModeSettingsDisableLastRefused(t *testing.T) {
	m := newTestSwitchModel()
	// Disable all except anime
	for _, k := range m.configuredModes {
		if k != "anime" {
			m.disabledModes[k] = true
		}
	}
	m.updateEffectiveModes()

	// Find index of anime
	animeIdx := 0
	for i, k := range m.configuredModes {
		if k == "anime" {
			animeIdx = i
			break
		}
	}

	m.toggleModeAtIndex(animeIdx)
	if m.disabledModes["anime"] {
		t.Error("disabling the last effective mode should be refused")
	}
	if m.activeToast == nil || m.activeToast.Message != "✗ keep at least one mode on" {
		t.Errorf("toast = %v, want '✗ keep at least one mode on'", m.activeToast)
	}
}

func TestModeSettingsDisablingCurrentSwitchesAway(t *testing.T) {
	m := newTestSwitchModel()
	m.appMode = provider.ModeAnime

	// Find anime index
	animeIdx := 0
	for i, k := range m.configuredModes {
		if k == "anime" {
			animeIdx = i
			break
		}
	}

	m.toggleModeAtIndex(animeIdx)
	if m.appMode == provider.ModeAnime {
		t.Error("active mode should switch away when current mode is disabled")
	}
	if m.appMode != provider.ModeCartoon {
		t.Errorf("appMode = %s, want cartoon", m.appMode)
	}
}

func TestModeSettingsDefaultModeSkipsUnavailableAndResetsOnDisable(t *testing.T) {
	m := newTestSwitchModel()
	// Remove jellyfin provider from registry so it's unavailable
	m.registry = &provider.Registry{}
	m.registry.Register(&fakeProviderForSwitch{name: "anikoto", modes: []provider.Mode{{Name: provider.ModeAnime}}})
	m.registry.Register(&fakeProviderForSwitch{name: "cartoon_p", modes: []provider.Mode{{Name: provider.ModeCartoon}}})

	options := m.availableStartupModes()
	for _, opt := range options {
		if opt.Key == "jellyfin" {
			t.Errorf("jellyfin must not be in available startup modes when unavailable")
		}
	}
	// Set default mode to anime
	m.defaultMode = "anime"

	// Find anime index in configuredModes and disable it
	animeIdx := 0
	for i, k := range m.configuredModes {
		if k == "anime" {
			animeIdx = i
			break
		}
	}
	m.toggleModeAtIndex(animeIdx)
	if m.defaultMode != "last" {
		t.Errorf("defaultMode must reset to 'last' when default mode is disabled, got %q", m.defaultMode)
	}
}

func TestHelpOverlayChaptersAndReader(t *testing.T) {
	m := newTestSwitchModel()
	accent := m.currentAccent()

	// Chapters view help overlay
	m.activeView = viewChapters
	out := m.renderHelpOverlayContent(100, accent)
	if !strings.Contains(out, "Chapters") {
		t.Errorf("expected Chapters in help overlay on viewChapters, got:\n%s", out)
	}
	if strings.Contains(out, "Global  Global") || strings.Contains(out, "Global   Global") {
		t.Errorf("help overlay must not duplicate Global header, got:\n%s", out)
	}

	// Reader view help overlay
	m.activeView = viewReader
	outReader := m.renderHelpOverlayContent(100, accent)
	if !strings.Contains(outReader, "Reader") {
		t.Errorf("expected Reader in help overlay on viewReader, got:\n%s", outReader)
	}
}
