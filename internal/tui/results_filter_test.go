package tui

// Behavior pins for the Results screen: space starts a new search from
// the results list, and / filters the results by title.

import (
	"testing"

	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"kari/internal/provider"
)

func resultsFilterModel() *modelImpl {
	m := &modelImpl{
		keys:       defaultKeyMap(),
		appMode:    provider.ModeAnime,
		activeView: viewSearch,
		queryInput: textinput.New(),
	}
	m.seriesResults = []provider.SearchResult{
		{Title: "Frieren: Beyond Journey's End", ID: "154587", Year: "2023"},
		{Title: "Sousou no Frieren 2nd Season", ID: "182255", Year: "2026"},
		{Title: "One Piece", ID: "21", Year: "1999"},
	}
	m.seriesList = list.New(seriesToItems(m.seriesResults), list.NewDefaultDelegate(), 80, 16)
	m.seriesList.SetFilteringEnabled(false)
	return m
}

func TestResultsHorizontalArrowsDoNotPageTheList(t *testing.T) {
	m := resultsFilterModel()
	m.seriesResults = make([]provider.SearchResult, 24)
	for i := range m.seriesResults {
		m.seriesResults[i] = provider.SearchResult{Title: "Title", ID: string(rune('a' + i))}
	}
	m.seriesList.SetItems(seriesToItems(m.seriesResults))
	m.seriesList.SetSize(80, 5)
	for _, key := range []tea.KeyType{tea.KeyLeft, tea.KeyRight} {
		m.updateSearch(tea.KeyMsg{Type: key})
		if got := m.selectedSeriesIndex(); got != 0 {
			t.Fatalf("horizontal arrow moved result selection to %d; want 0", got)
		}
	}
}

func specialKey(t tea.KeyType, s string) tea.KeyMsg {
	return tea.KeyMsg{Type: t, Runes: []rune(s)}
}

// TestSearchBoundaryScrollDoesNotRefetchPoster proves scrolling past the top
// or bottom of the results list does no poster work. triggerSearchPoster bumps
// searchPosterOpID via clearSearchPoster on every call, so an unchanged opID
// means no cancel/refetch cycle was scheduled for a redundant scroll.
func TestSearchBoundaryScrollDoesNotRefetchPoster(t *testing.T) {
	const seedOpID = 7
	newModel := func() *modelImpl {
		m := resultsFilterModel()
		m.searchPosterOpID = seedOpID
		return m
	}

	// At the top, up must be a no-op: no command, no poster slot churn.
	top := newModel()
	_, cmd := top.updateSearch(tea.KeyMsg{Type: tea.KeyUp})
	if cmd != nil {
		t.Errorf("up at top returned a command %v, want none", cmd)
	}
	if top.searchPosterOpID != seedOpID {
		t.Errorf("up at top bumped poster opID to %d, want %d", top.searchPosterOpID, seedOpID)
	}

	// At the bottom, down must likewise do nothing.
	bottom := newModel()
	bottom.seriesList.Select(len(bottom.seriesResults) - 1)
	_, cmd = bottom.updateSearch(tea.KeyMsg{Type: tea.KeyDown})
	if cmd != nil {
		t.Errorf("down at bottom returned a command %v, want none", cmd)
	}
	if bottom.searchPosterOpID != seedOpID {
		t.Errorf("down at bottom bumped poster opID to %d, want %d", bottom.searchPosterOpID, seedOpID)
	}

	// A real move still refreshes the poster exactly once (one opID bump).
	moved := newModel()
	_, _ = moved.updateSearch(tea.KeyMsg{Type: tea.KeyDown})
	if got := moved.selectedSeriesIndex(); got != 1 {
		t.Fatalf("after down, index = %d, want 1", got)
	}
	if moved.searchPosterOpID != seedOpID+1 {
		t.Errorf("real move bumped poster opID to %d, want %d", moved.searchPosterOpID, seedOpID+1)
	}
}

// / enters filter mode instead of focusing the search input.
func TestResultsSlashStartsFilter(t *testing.T) {
	m := resultsFilterModel()
	mdl, _ := m.updateSearch(specialKey(tea.KeyRunes, "/"))
	m = mdl.(*modelImpl)
	if !m.resultsFiltering {
		t.Fatal("resultsFiltering = false after /, want true")
	}
	if m.queryInput.Focused() {
		t.Error("search input must not steal focus when / starts filtering")
	}
}

// Typing narrows the list while keeping original indices for selection.
func TestResultsFilterNarrowsList(t *testing.T) {
	m := resultsFilterModel()
	mdl, _ := m.updateSearch(specialKey(tea.KeyRunes, "/"))
	m = mdl.(*modelImpl)
	for _, r := range []string{"f", "r", "i"} {
		mdl, _ = m.updateSearch(specialKey(tea.KeyRunes, r))
		m = mdl.(*modelImpl)
	}
	visible, idxs := m.visibleSeriesResults()
	if len(visible) != 2 {
		t.Fatalf("visible results = %d, want 2 (both Frieren rows)", len(visible))
	}
	if idxs[0] != 0 || idxs[1] != 1 {
		t.Fatalf("original indices = %v, want [0 1]", idxs)
	}
	if got := m.selectedSeriesIndex(); got != 0 {
		t.Fatalf("selectedSeriesIndex = %d, want 0 (original index)", got)
	}
}

// Enter on a filtered row opens the original result.
func TestResultsFilterEnterOpensOriginal(t *testing.T) {
	m := resultsFilterModel()
	mdl, _ := m.updateSearch(specialKey(tea.KeyRunes, "/"))
	m = mdl.(*modelImpl)
	for _, r := range []string{"o", "n", "e"} {
		mdl, _ = m.updateSearch(specialKey(tea.KeyRunes, r))
		m = mdl.(*modelImpl)
	}
	visible, _ := m.visibleSeriesResults()
	if len(visible) != 1 || visible[0].Title != "One Piece" {
		t.Fatalf("visible = %+v, want only One Piece", visible)
	}
	// Selection must resolve into the full list, not the filtered one.
	m.selectedSeries = &m.seriesResults[m.selectedSeriesIndex()]
	if m.selectedSeries.Title != "One Piece" {
		t.Fatalf("selected = %q, want One Piece", m.selectedSeries.Title)
	}
}

// space focuses the search input for a new search.
func TestResultsSpaceStartsNewSearch(t *testing.T) {
	m := resultsFilterModel()
	mdl, _ := m.updateSearch(specialKey(tea.KeyRunes, " "))
	m = mdl.(*modelImpl)
	if !m.queryInput.Focused() {
		t.Fatal("space on results must focus the search input")
	}
}

// Typing a filter query must not trigger global single-key bindings:
// "q" extends the query instead of quitting, "h" instead of history.
func TestResultsFilterTypingSkipsGlobalKeys(t *testing.T) {
	m := resultsFilterModel()
	mdl, _ := m.updateSearch(specialKey(tea.KeyRunes, "/"))
	m = mdl.(*modelImpl)
	if cmd, handled := m.handleGlobalKeys(specialKey(tea.KeyRunes, "q")); handled || cmd != nil {
		t.Fatal("q while filtering must fall through to the filter, not quit")
	}
	if cmd, handled := m.handleGlobalKeys(specialKey(tea.KeyRunes, "h")); handled || cmd != nil {
		t.Fatal("h while filtering must fall through to the filter, not history")
	}
}

// esc clears an active filter via the global back handler.
func TestResultsEscClearsFilter(t *testing.T) {
	m := resultsFilterModel()
	m.resultsFilter = "fri"
	m.resultsFiltering = true
	m.refilterSeriesList()
	if cmd, handled := m.handleGlobalKeys(specialKey(tea.KeyEsc, "esc")); !handled || cmd != nil {
		t.Fatal("esc with an active filter must be consumed by clearActiveFilter")
	}
	if m.resultsFilter != "" || m.resultsFiltering {
		t.Fatal("filter must be cleared after esc")
	}
	visible, _ := m.visibleSeriesResults()
	if len(visible) != 3 {
		t.Fatalf("visible results after clear = %d, want 3", len(visible))
	}
}

func TestResultsDNavigatesToDownloads(t *testing.T) {
	m := resultsFilterModel()
	mdl, _ := m.updateSearch(specialKey(tea.KeyRunes, "d"))
	m = mdl.(*modelImpl)
	if m.activeView != viewDownloads {
		t.Fatalf("activeView = %v, want viewDownloads after pressing 'd'", m.activeView)
	}
}
