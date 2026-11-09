package tui

import (
	"context"
	"testing"

	"kari/internal/provider"
	"kari/internal/service"
	"kari/internal/util"
)

// aliasProvider advertises a Presenter alias so settings rows can be checked
// against the user-facing name.
type aliasProvider struct {
	fakeProviderForSwitch
	alias string
}

func (p *aliasProvider) Alias() string { return p.alias }

type mangaSettingsProvider struct{ fakeProviderForSwitch }

func (*mangaSettingsProvider) FetchChapters(context.Context, provider.SearchResult) ([]provider.MangaChapter, error) {
	return nil, nil
}

func (*mangaSettingsProvider) FetchPages(context.Context, provider.MangaChapter) ([]provider.MangaPage, error) {
	return nil, nil
}

func TestProviderRowsIncludeLiveProviders(t *testing.T) {
	registry := &provider.Registry{}
	registry.Register(&aliasProvider{
		fakeProviderForSwitch: fakeProviderForSwitch{
			name:  "famelack",
			modes: []provider.Mode{{Name: provider.ModeLive, Priority: 2}},
		},
		alias: "Famelack",
	})
	m := &modelImpl{registry: registry, disabledProviders: map[string]bool{}}

	rows := m.providerRows()
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1 live provider", len(rows))
	}
	row := rows[0]
	if row.Section != "Live" || row.Mode != provider.ModeLive {
		t.Errorf("section/mode = %q/%q, want Live/live", row.Section, row.Mode)
	}
	if row.ID != "famelack" || row.Name != "Famelack" || row.Key != "live:famelack" {
		t.Errorf("row identity = %+v", row)
	}
	if !row.Enabled || !row.Available {
		t.Errorf("row should start enabled and available: %+v", row)
	}

	m.disabledProviders["live:famelack"] = true
	rows = m.providerRows()
	if len(rows) != 1 || rows[0].Enabled {
		t.Errorf("mode-scoped disable must turn the live row off: %+v", rows)
	}
}

func TestLoadedStartupAddonMakesStremioModeAvailable(t *testing.T) {
	registry := &provider.Registry{}
	m := &modelImpl{
		registry:        registry,
		configuredModes: []string{"anime", "stremio"},
		disabledModes:   make(map[string]bool),
	}
	addon := &fakeProviderForSwitch{
		name:  "configured-addon",
		modes: []provider.Mode{{Name: provider.ModeStremio}},
	}

	mdl, _ := m.updateSettings(startupAddonLoadedMsg{addon: addon})
	m = mdl.(*modelImpl)
	if !m.isModeAvailable(string(provider.ModeStremio)) {
		t.Fatal("Stremio mode should be available after its configured addon loads")
	}
	if len(m.modes) != 1 || m.modes[0] != provider.ModeStremio {
		t.Fatalf("effective modes = %v, want [stremio]", m.modes)
	}
}

func TestReadingProviderToggleIsIndependentAndClearsSearchCache(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	registry := &provider.Registry{}
	registry.Register(&mangaSettingsProvider{fakeProviderForSwitch{
		name:  "weebcentral",
		modes: []provider.Mode{{Name: provider.ModeManga, Priority: 1}},
	}})
	registry.Register(&mangaSettingsProvider{fakeProviderForSwitch{
		name:  "atsu",
		modes: []provider.Mode{{Name: provider.ModeManga, Priority: 2}},
	}})
	cache := util.NewBoundedCache[searchCacheEntry](10)
	cache.Set("manga:one piece", searchCacheEntry{
		results: []provider.SearchResult{{Title: "cached from WeebCentral", Provider: "weebcentral"}},
	})
	m := &modelImpl{
		registry:          registry,
		mediaService:      service.NewMediaService(registry),
		disabledProviders: make(map[string]bool),
		searchCache:       cache,
	}

	index := -1
	for i, row := range m.providerRows() {
		if row.Mode == provider.ModeManga && row.ID == "atsu" {
			if row.Radio {
				t.Fatal("reading providers must use independent toggles")
			}
			index = i
			break
		}
	}
	if index < 0 {
		t.Fatal("Atsu manga provider row not found")
	}
	m.toggleProviderAtIndex(index)

	if _, ok := cache.Get("manga:one piece"); ok {
		t.Fatal("provider toggle must clear cached manga search results")
	}
	if !m.isProviderDisabledKey(provider.ModeManga, "atsu") {
		t.Fatal("toggling Atsu off should disable Atsu")
	}
	if m.isProviderDisabledKey(provider.ModeManga, "weebcentral") {
		t.Fatal("toggling Atsu should leave WeebCentral enabled")
	}
}
