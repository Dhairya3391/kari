package tui

// Covers the Episodes screen keys that were advertised but dead: / filter
// typing, g/G jump, and cursor clamping on filtered lists.

import (
	"testing"

	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"

	"kari/internal/provider"
)

func episodesTestModel() *modelImpl {
	return &modelImpl{
		keys:        defaultKeyMap(),
		activeView:  viewEpisodes,
		episodeList: list.New(nil, list.NewDefaultDelegate(), 80, 16),
		episodeResults: []provider.Episode{
			{Season: 1, Episode: 1, Title: "The Name of the Game"},
			{Season: 1, Episode: 2, Title: "Cherry"},
			{Season: 1, Episode: 3, Title: "Get Some"},
		},
	}
}

func runeKey(s string) tea.KeyMsg {
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
}

func TestEpisodesTopBottomKeys(t *testing.T) {
	m := episodesTestModel()
	m.seasonEpisodeIndex = 1

	mdl, _ := m.updateEpisodes(runeKey("g"))
	m = mdl.(*modelImpl)
	if m.seasonEpisodeIndex != 0 {
		t.Errorf("g should jump to first, got %d", m.seasonEpisodeIndex)
	}

	mdl, _ = m.updateEpisodes(runeKey("G"))
	m = mdl.(*modelImpl)
	if m.seasonEpisodeIndex != 2 {
		t.Errorf("G should jump to last, got %d", m.seasonEpisodeIndex)
	}
}

func TestEpisodesFilterNarrowsAndClears(t *testing.T) {
	m := episodesTestModel()

	mdl, _ := m.updateEpisodes(runeKey("/"))
	m = mdl.(*modelImpl)
	if !m.episodeFiltering {
		t.Fatalf("/ should start filtering")
	}
	for _, r := range "cherry" {
		mdl, _ = m.updateEpisodes(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		m = mdl.(*modelImpl)
	}
	if m.episodeFilter != "cherry" {
		t.Fatalf("filter = %q, want cherry", m.episodeFilter)
	}
	eps, idxs := m.currentSeasonEpisodes()
	if len(eps) != 1 || idxs[0] != 1 {
		t.Fatalf("filtered list = %+v %+v, want [Cherry] [1]", eps, idxs)
	}
	if m.seasonEpisodeIndex != 0 {
		t.Errorf("cursor should clamp into filtered list, got %d", m.seasonEpisodeIndex)
	}

	// esc cancels via the global filter hook.
	if !m.clearActiveFilter() {
		t.Fatalf("esc should clear the episode filter")
	}
	if m.episodeFilter != "" || m.episodeFiltering {
		t.Errorf("filter not cleared: %q %v", m.episodeFilter, m.episodeFiltering)
	}
	eps, _ = m.currentSeasonEpisodes()
	if len(eps) != 3 {
		t.Errorf("cleared filter should restore all rows, got %d", len(eps))
	}
}

func TestEpisodesSpaceTogglesSelectMode(t *testing.T) {
	m := episodesTestModel()
	m.seasonEpisodeIndex = 0

	// Physical spacebar (reporting " ") enters select mode and selects current episode.
	mdl, _ := m.updateEpisodes(runeKey(" "))
	m = mdl.(*modelImpl)
	if !m.selectMode {
		t.Fatalf("space must enter select mode")
	}
	if _, ok := m.selectedEpisodes[0]; !ok {
		t.Fatalf("episode 0 must be selected upon entering select mode, got %v", m.selectedEpisodes)
	}

	// Space toggles off the selected episode
	mdl, _ = m.updateEpisodes(runeKey(" "))
	m = mdl.(*modelImpl)
	if _, ok := m.selectedEpisodes[0]; ok {
		t.Fatalf("second space must unselect episode 0, got %v", m.selectedEpisodes)
	}

	// ctrl+a selects all
	mdl, _ = m.updateEpisodes(specialKey(tea.KeyCtrlA, "ctrl+a"))
	m = mdl.(*modelImpl)
	if len(m.selectedEpisodes) != 3 {
		t.Fatalf("ctrl+a must select all 3 episodes, got %d", len(m.selectedEpisodes))
	}

	// ctrl+d deselects all
	mdl, _ = m.updateEpisodes(specialKey(tea.KeyCtrlD, "ctrl+d"))
	m = mdl.(*modelImpl)
	if len(m.selectedEpisodes) != 0 {
		t.Fatalf("ctrl+d must deselect all, got %d", len(m.selectedEpisodes))
	}

	// esc exits select mode
	mdl, _ = m.updateEpisodes(specialKey(tea.KeyEsc, "esc"))
	m = mdl.(*modelImpl)
	if m.selectMode {
		t.Fatalf("esc must exit select mode")
	}
}

func TestEpisodesEscCancelsSelectModeThroughGlobalKeys(t *testing.T) {
	m := episodesTestModel()
	m.backStack = []viewState{viewSearch}
	m.selectMode = true
	m.selectedEpisodes = map[int]struct{}{0: {}}

	// First Esc: cancels select mode, stays on viewEpisodes
	mdl, _ := m.Update(specialKey(tea.KeyEsc, "esc"))
	m = mdl.(*modelImpl)
	if m.selectMode {
		t.Errorf("selectMode must be false after first esc")
	}
	if m.activeView != viewEpisodes {
		t.Errorf("activeView = %v, want viewEpisodes after first esc", m.activeView)
	}

	// Second Esc: now goes back to viewSearch
	mdl, _ = m.Update(specialKey(tea.KeyEsc, "esc"))
	m = mdl.(*modelImpl)
	if m.activeView != viewSearch {
		t.Errorf("activeView = %v, want viewSearch after second esc", m.activeView)
	}
}

func TestEpisodesDNavigatesToDownloads(t *testing.T) {
	m := episodesTestModel()
	mdl, _ := m.updateEpisodes(runeKey("d"))
	m = mdl.(*modelImpl)
	if m.activeView != viewDownloads {
		t.Fatalf("activeView = %v, want viewDownloads after pressing 'd'", m.activeView)
	}
}

func TestEpisodesBatchDownloadStaysOnEpisodesAndResetsSelection(t *testing.T) {
	m := episodesTestModel()
	m.selectedSeries = &provider.SearchResult{Title: "The Boys", ID: "123"}

	// Enter select mode on episode 0
	mdl, _ := m.updateEpisodes(runeKey(" "))
	m = mdl.(*modelImpl)
	if !m.selectMode || len(m.selectedEpisodes) != 1 {
		t.Fatalf("select mode must be active with 1 episode selected")
	}

	// Start batch download with 'D'
	mdl, _ = m.updateEpisodes(runeKey("D"))
	m = mdl.(*modelImpl)

	// Select mode must be cleared and selections reset
	if m.selectMode {
		t.Errorf("selectMode must be false after starting download")
	}
	if len(m.selectedEpisodes) != 0 {
		t.Errorf("selectedEpisodes must be cleared, got %v", m.selectedEpisodes)
	}
	// Download must be in background: loading is false, batchInProgress is true
	if m.loading {
		t.Errorf("loading must be false for background batch download")
	}
	if !m.batchInProgress {
		t.Errorf("batchInProgress must be true")
	}
	if m.downloadOpID == 0 {
		t.Errorf("downloadOpID must be set")
	}
	if m.activeView != viewEpisodes {
		t.Errorf("activeView must remain viewEpisodes, got %v", m.activeView)
	}

	// Episodes list must remain fully visible (not hidden by loading state)
	eps, _ := m.currentSeasonEpisodes()
	if len(eps) != 3 {
		t.Errorf("episodes list must remain intact with 3 episodes, got %d", len(eps))
	}
}

func TestCollectDownloadsDataActiveBatchAndSingle(t *testing.T) {
	m := episodesTestModel()
	m.selectedSeries = &provider.SearchResult{Title: "Supernatural"}
	m.batchInProgress = true
	m.batchCurrent = 1
	m.batchTotal = 3
	m.batchEpisodeProgress = 0.45
	m.downloadSpeed = "3.5 MB/s"
	m.downloadETA = "2m left"

	data := m.collectDownloadsData(100, 30, m.currentAccent())
	if len(data.Active) != 3 {
		t.Fatalf("expected 3 active download rows (1 active + 2 queued), got %d", len(data.Active))
	}
	if data.Active[0].Queued {
		t.Errorf("first item must not be queued")
	}
	if data.Active[0].Progress != 0.45 {
		t.Errorf("first item progress = %v, want 0.45", data.Active[0].Progress)
	}
	if !data.Active[1].Queued || !data.Active[2].Queued {
		t.Errorf("subsequent items must be queued")
	}

	// Test pause with 'p'
	cancelled := false
	m.batchCancel = func() { cancelled = true }
	m.activeView = viewDownloads
	mdl, _ := m.updateDownloads(runeKey("p"))
	m = mdl.(*modelImpl)
	if !m.downloadPaused {
		t.Errorf("pressing 'p' must pause download")
	}
	if !cancelled {
		t.Errorf("pausing must cancel active process context")
	}

	// Test resume with 'p'
	m.batchEpisodes = []provider.Episode{{Title: "Ep 1"}, {Title: "Ep 2"}, {Title: "Ep 3"}}
	mdl, _ = m.updateDownloads(runeKey("p"))
	m = mdl.(*modelImpl)
	if m.downloadPaused {
		t.Errorf("pressing 'p' when paused must resume download")
	}

	// Test cancelling queued item with 'x' (select index 1 which is queued).
	// Cancel is destructive, so it now needs the double-press confirmation.
	m.downloadsIndex = 1
	mdl, _ = m.updateDownloads(runeKey("x"))
	m = mdl.(*modelImpl)
	if !m.confirmStop {
		t.Errorf("first 'x' must arm the cancel confirmation")
	}
	if len(m.batchEpisodes) != 3 {
		t.Errorf("first 'x' must not change the queue yet, got %d episodes", len(m.batchEpisodes))
	}
	mdl, _ = m.updateDownloads(runeKey("x"))
	m = mdl.(*modelImpl)
	if len(m.batchEpisodes) != 2 {
		t.Errorf("confirmed 'x' must reduce batchEpisodes length to 2, got %d", len(m.batchEpisodes))
	}

	// Test cancel all with 'X' (also double-press confirmed).
	allCancelled := false
	m.batchCancel = func() { allCancelled = true }
	mdl, _ = m.updateDownloads(runeKey("X"))
	m = mdl.(*modelImpl)
	if !m.confirmStop {
		t.Errorf("first 'X' must arm the cancel-all confirmation")
	}
	mdl, _ = m.updateDownloads(runeKey("X"))
	m = mdl.(*modelImpl)
	if !allCancelled {
		t.Errorf("confirmed 'X' must cancel batch")
	}
	if m.batchInProgress {
		t.Errorf("batchInProgress must be false after 'X'")
	}
}
