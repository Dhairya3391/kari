package tui

import (
	"testing"

	"kari/internal/provider"
)

// aliasProvider advertises a Presenter alias so settings rows can be checked
// against the user-facing name.
type aliasProvider struct {
	fakeProviderForSwitch
	alias string
}

func (p *aliasProvider) Alias() string { return p.alias }

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
