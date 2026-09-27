package tui

import (
	"context"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"kari/internal/provider"
)

type emptyQueryStubProvider struct {
	name  string
	modes []provider.Mode
}

func (p *emptyQueryStubProvider) Name() string           { return p.name }
func (p *emptyQueryStubProvider) Modes() []provider.Mode { return p.modes }
func (p *emptyQueryStubProvider) Features(mode provider.ContentType) provider.Features {
	return provider.Features{
		AllowEmptyQuery: true,
	}
}
func (p *emptyQueryStubProvider) Search(ctx context.Context, query string, mode provider.ContentType) ([]provider.SearchResult, error) {
	return []provider.SearchResult{
		{Title: "All Live Item 1", ID: "pp-live:1", Type: provider.ModeLive},
		{Title: "All Live Item 2", ID: "pp-live:2", Type: provider.ModeLive},
	}, nil
}
func (p *emptyQueryStubProvider) FetchEpisodes(ctx context.Context, series provider.SearchResult) ([]provider.Episode, error) {
	return nil, nil
}
func (p *emptyQueryStubProvider) ResolveSource(ctx context.Context, mediaID string, episode provider.Episode) ([]provider.MediaSource, error) {
	return nil, nil
}

func TestEmptySearchAllowedForLiveAndJellyfin(t *testing.T) {
	m := newTestSwitchModel()
	m.registry.Register(&emptyQueryStubProvider{
		name:  "live_empty_stub",
		modes: []provider.Mode{{Name: provider.ModeLive}},
	})
	m.appMode = provider.ModeLive
	m.activeView = viewSearch

	// Pressing enter on Search Home with empty query for Live triggers search
	mdl, cmd := m.updateSearch(tea.KeyMsg{Type: tea.KeyEnter})
	m = mdl.(*modelImpl)
	if !m.loading {
		t.Error("expected loading state when pressing enter on empty query in ModeLive")
	}
	if cmd == nil {
		t.Error("expected non-nil cmd for empty search in ModeLive")
	}
}
