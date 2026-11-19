package tui

import (
	"context"
	"errors"
	"testing"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"kari/internal/provider"
	"kari/internal/service"
	"kari/internal/settings"
)

func TestAddonSettingsPersistAcrossRestart(t *testing.T) {
	tempHome := t.TempDir()
	t.Setenv("HOME", tempHome)

	const manifestURL = "https://addon.example/manifest.json"
	m := newTestSwitchModel()
	m.stremioAddonManifests = []string{manifestURL}
	m.disabledProviders = map[string]bool{"addon-abc123": true}
	m.saveSettings()

	saved := settings.Load()
	if saved == nil {
		t.Fatal("settings.Load() returned nil")
	}
	if len(saved.StremioAddonManifests) != 1 || saved.StremioAddonManifests[0] != manifestURL {
		t.Fatalf("manifests = %+v, want [%s]", saved.StremioAddonManifests, manifestURL)
	}
	if !saved.DisabledProviderSet()["addon-abc123"] {
		t.Fatal("disabled addon key was not persisted")
	}
}

// fakeConfiguredAddon stands in for a registered Stremio addon so the registry
// can be asked whether a provider name belongs to a multi-mode configured addon.
type fakeConfiguredAddon struct {
	name string
}

func (f *fakeConfiguredAddon) Name() string { return f.name }

func (f *fakeConfiguredAddon) Modes() []provider.Mode {
	return []provider.Mode{{Name: provider.ModeStremio}}
}

func (f *fakeConfiguredAddon) MatchesConfiguration(value string) bool { return value == f.name }

func (f *fakeConfiguredAddon) Search(context.Context, string, provider.ContentType) ([]provider.SearchResult, error) {
	return nil, provider.ErrNoResults
}

func (f *fakeConfiguredAddon) FetchEpisodes(context.Context, provider.SearchResult) ([]provider.Episode, error) {
	return nil, provider.ErrNoEpisodes
}

func (f *fakeConfiguredAddon) ResolveSource(context.Context, string, provider.Episode) ([]provider.MediaSource, error) {
	return nil, provider.ErrNoSources
}

func TestConfiguredAddonDisableIsGlobal(t *testing.T) {
	const addonName = "addon-abc123"
	registry := &provider.Registry{}
	registry.Register(&fakeConfiguredAddon{name: addonName})
	registry.Register(&fakeProviderForSwitch{name: "pengu", modes: []provider.Mode{{Name: provider.ModeMovies}}})
	m := &modelImpl{disabledProviders: map[string]bool{}, registry: registry}

	// Disabling an addon in Stremio mode must hide it in Movies and TV too.
	m.disabledProviders["stremio:"+addonName] = true
	for _, mode := range []provider.ContentType{provider.ModeStremio, provider.ModeMovies, provider.ModeTV} {
		if !m.isProviderDisabledKey(mode, addonName) {
			t.Errorf("addon not disabled in %s mode", mode)
		}
	}

	deleteModeScopedKeys(m.disabledProviders, addonName)
	m.disabledProviders[addonName] = true
	if !m.isProviderDisabledKey(provider.ModeMovies, addonName) {
		t.Fatal("global addon key did not disable the addon")
	}
	if m.isProviderDisabledKey(provider.ModeMovies, "pengu") {
		t.Fatal("ordinary provider was treated as a configured addon")
	}
}

func TestExitInputModeClearsRebindTarget(t *testing.T) {
	m := newTestSwitchModel()
	m.activeView = viewSettings
	m.addonInputOpen = true
	m.addonRebindingURL = "addon-old"
	m.addonTesting = true
	m.addonPendingURL = "https://old.example/manifest.json"

	if !m.exitInputMode() {
		t.Fatal("exitInputMode did not consume the Esc")
	}
	if m.addonRebindingURL != "" {
		t.Fatalf("rebind target survived Esc: %q", m.addonRebindingURL)
	}
	if m.addonTesting || m.addonPendingURL != "" || m.addonInputOpen {
		t.Fatalf("addon edit state not cleared: testing=%v pending=%q open=%v",
			m.addonTesting, m.addonPendingURL, m.addonInputOpen)
	}
}

func TestSyncDisabledProvidersGlobalizesLegacyAddonKey(t *testing.T) {
	const addonName = "addon-abc123"
	registry := &provider.Registry{}
	registry.Register(&fakeConfiguredAddon{name: addonName})
	svc := service.NewMediaService(registry)
	m := &modelImpl{
		registry:          registry,
		mediaService:      svc,
		disabledProviders: map[string]bool{"stremio:" + addonName: true},
	}

	m.syncDisabledProvidersToService()

	for _, mode := range []provider.ContentType{provider.ModeStremio, provider.ModeMovies, provider.ModeTV} {
		if !svc.IsProviderDisabled(mode, addonName) {
			t.Errorf("legacy mode-scoped key did not disable addon in %s mode", mode)
		}
	}
}

func TestStaleAddonTestResultIsIgnored(t *testing.T) {
	const manifest = "https://old.example/manifest.json"
	registry := &provider.Registry{}
	registry.Register(&fakeConfiguredAddon{name: "addon-old"})
	m := &modelImpl{
		registry:              registry,
		stremioAddonManifests: []string{manifest},
		addonTesting:          true,
		addonTestOpID:         2,
		addonPendingURL:       manifest,
		disabledProviders:     map[string]bool{},
	}

	mdl, _ := m.updateSettings(addonTestDoneMsg{
		opID:        1,
		manifestURL: manifest,
		addon:       &fakeConfiguredAddon{name: "addon-new"},
	})
	m = mdl.(*modelImpl)
	if len(m.stremioAddonManifests) != 1 || m.stremioAddonManifests[0] != manifest {
		t.Fatalf("stale result changed manifests: %v", m.stremioAddonManifests)
	}
	if !m.addonTesting {
		t.Fatal("stale result consumed the in-flight check")
	}
}

func TestDeleteAddonDuringCheckIsIgnored(t *testing.T) {
	const manifest = "https://addon.example/manifest.json"
	registry := &provider.Registry{}
	registry.Register(&fakeConfiguredAddon{name: "addon-abc123"})
	m := &modelImpl{
		registry:              registry,
		stremioAddonManifests: []string{manifest},
		settingsCategory:      CategoryAddons,
		settingsIndex:         1,
		addonTesting:          true,
		addonPendingURL:       manifest,
		addonTestOpID:         1,
		disabledProviders:     map[string]bool{},
	}

	m.updateSettings(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'d'}})

	if len(m.stremioAddonManifests) != 1 {
		t.Fatalf("addon deleted during an in-flight check: %v", m.stremioAddonManifests)
	}
}

func TestFailedRebindRetryStillReplacesAddon(t *testing.T) {
	const oldManifest = "https://old.example/manifest.json"
	const newManifest = "https://new.example/manifest.json"
	registry := &provider.Registry{}
	registry.Register(&fakeConfiguredAddon{name: "addon-old"})
	m := &modelImpl{
		registry:              registry,
		stremioAddonManifests: []string{oldManifest},
		settingsCategory:      CategoryAddons,
		settingsIndex:         1,
		addonInputOpen:        true,
		addonTesting:          true,
		addonTestOpID:         1,
		addonPendingURL:       newManifest,
		addonRebindingURL:     oldManifest,
		disabledProviders:     map[string]bool{},
		addonManifestTester: func(context.Context, string) (provider.Provider, error) {
			return nil, errors.New("check unavailable")
		},
	}
	m.addonInput = textinput.New()
	m.addonInput.SetValue(newManifest)

	mdl, _ := m.updateSettings(addonTestDoneMsg{
		opID:        1,
		manifestURL: newManifest,
		replacing:   oldManifest,
		err:         errors.New("manifest fetch failed"),
	})
	m = mdl.(*modelImpl)
	if m.addonRebindingURL != oldManifest {
		t.Fatalf("failed check dropped the rebind target: %q", m.addonRebindingURL)
	}

	_, cmd := m.updateSettings(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("retry did not start a check")
	}
	retry, ok := cmd().(addonTestDoneMsg)
	if !ok {
		t.Fatalf("retry produced %T, want addonTestDoneMsg", cmd())
	}
	if retry.replacing != oldManifest {
		t.Fatalf("retry replacing = %q, want %q", retry.replacing, oldManifest)
	}
	if retry.manifestURL != newManifest {
		t.Fatalf("retry manifest = %q, want %q", retry.manifestURL, newManifest)
	}
}

func TestRebindDisabledProviderKeys(t *testing.T) {
	m := &modelImpl{disabledProviders: map[string]bool{
		"addon-old":         true,
		"stremio:addon-old": true,
		"anime:anicine":     true,
	}}
	m.rebindDisabledProviderKeys("addon-old", "addon-new")

	for _, key := range []string{"addon-new", "stremio:addon-new", "anime:anicine"} {
		if !m.disabledProviders[key] {
			t.Errorf("disabled key %q was not preserved", key)
		}
	}
	for _, key := range []string{"addon-old", "stremio:addon-old"} {
		if m.disabledProviders[key] {
			t.Errorf("stale disabled key %q survived rebind", key)
		}
	}
}

func TestCanonicalAddonURL(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{"full manifest", "https://addon.example/manifest.json?token=x", "https://addon.example?token=x"},
		{"bare root", "https://addon.example/", "https://addon.example"},
		{"fragment dropped", "https://addon.example/manifest.json#top", "https://addon.example"},
		{"host case ignored", "HTTPS://Addon.Example/manifest.json?token=x", "https://addon.example?token=x"},
		{"query case kept", "https://addon.example/manifest.json?token=X", "https://addon.example?token=X"},
		{"path case kept", "https://addon.example/Config/manifest.json", "https://addon.example/Config"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := canonicalAddonURL(tt.raw); got != tt.want {
				t.Fatalf("canonicalAddonURL(%q) = %q, want %q", tt.raw, got, tt.want)
			}
		})
	}
	if canonicalAddonURL("https://addon.example/") != canonicalAddonURL("https://addon.example/manifest.json") {
		t.Fatal("bare and full manifest URLs did not collapse to one key")
	}
	if canonicalAddonURL("https://addon.example/manifest.json?token=x") == canonicalAddonURL("https://addon.example/") {
		t.Fatal("different configurations collapsed to one key")
	}
}
