package tui

// Covers the Episodes screen keys that were advertised but dead: / filter
// typing, g/G jump, and cursor clamping on filtered lists.

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"kari/internal/history"
	"kari/internal/model"
	"kari/internal/player"
	"kari/internal/provider"
	"kari/internal/service"
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

func TestEpisodesHorizontalArrowsChangeSeason(t *testing.T) {
	m := episodesTestModel()
	m.episodeResults = append(m.episodeResults,
		provider.Episode{Season: 2, Episode: 1, Title: "The Name of the Game"},
		provider.Episode{Season: 2, Episode: 2, Title: "Cherry"},
	)
	m.seasonEpisodeIndex = 1
	m.updateEpisodes(tea.KeyMsg{Type: tea.KeyRight})
	if m.activeSeason != 1 || m.seasonEpisodeIndex != 0 {
		t.Fatalf("Right selected season %d at episode %d, want season 1 episode 0", m.activeSeason, m.seasonEpisodeIndex)
	}
	m.updateEpisodes(tea.KeyMsg{Type: tea.KeyLeft})
	if m.activeSeason != 0 {
		t.Fatalf("Left selected season %d, want season 0", m.activeSeason)
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

func TestCollectDownloadsDataDone(t *testing.T) {
	m := episodesTestModel()
	m.doneDownloads = []DoneDownload{
		{Title: "One Piece — S01E01", Quality: "1080p", Size: "220 MB"},
	}

	data := m.collectDownloadsData(100, 30, m.currentAccent())
	if len(data.Done) != 1 {
		t.Fatalf("expected 1 completed download, got %d", len(data.Done))
	}
	if data.Done[0].Title != "One Piece — S01E01" {
		t.Errorf("data.Done[0].Title = %q", data.Done[0].Title)
	}

	// Press x to remove from history
	m.activeView = viewDownloads
	m.downloadsIndex = 0 // points to Done[0] since Active is empty
	mdl, _ := m.updateDownloads(runeKey("x"))
	m = mdl.(*modelImpl)
	if !m.confirmStop {
		t.Fatalf("expected confirmStop on first x press")
	}
	mdl, _ = m.updateDownloads(runeKey("x"))
	m = mdl.(*modelImpl)

	dataAfter := m.collectDownloadsData(100, 30, m.currentAccent())
	if len(dataAfter.Done) != 0 {
		t.Fatalf("expected 0 completed downloads after x, got %d", len(dataAfter.Done))
	}
}
func TestDownloadsIndexClampedOnDismiss(t *testing.T) {
	m := episodesTestModel()
	m.doneDownloads = []DoneDownload{
		{Title: "One Piece — S01E01", Quality: "1080p", Size: "220 MB"},
		{Title: "One Piece — S01E02", Quality: "1080p", Size: "225 MB"},
	}

	m.activeView = viewDownloads
	m.downloadsIndex = 1 // points to Done[1] (last item)

	// Confirm remove Done[1]
	mdl, _ := m.updateDownloads(runeKey("x"))
	m = mdl.(*modelImpl)
	mdl, _ = m.updateDownloads(runeKey("x"))
	m = mdl.(*modelImpl)

	if m.downloadsIndex != 0 {
		t.Errorf("downloadsIndex after dismissing last item = %d, want 0", m.downloadsIndex)
	}
	if len(m.doneDownloads) != 1 {
		t.Fatalf("expected 1 remaining done download, got %d", len(m.doneDownloads))
	}
	if m.doneDownloads[0].Title != "One Piece — S01E01" {
		t.Errorf("remaining done download title = %q, want S01E01", m.doneDownloads[0].Title)
	}
}

func TestEpisodesMarkCompleteToggle(t *testing.T) {
	store, err := history.NewStore(filepath.Join(t.TempDir(), "history.json"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(store.Close)

	m := episodesTestModel()
	m.historyStore = store
	m.appMode = provider.ModeTV
	m.selectedSeries = &provider.SearchResult{Title: "The Boys", MediaType: provider.MediaTypeTV}
	m.seasonEpisodeIndex = 0

	// 1. Press 'm' to mark complete
	mdl, _ := m.updateEpisodes(runeKey("m"))
	m = mdl.(*modelImpl)

	key := history.EntryKey{
		Title:     "The Boys",
		Mode:      string(provider.ModeTV),
		MediaType: provider.MediaTypeTV,
		Season:    1,
		Episode:   1,
	}
	entry, ok := store.Get(key)
	if !ok {
		t.Fatalf("expected entry in history store after marking complete")
	}
	if !entry.Complete {
		t.Errorf("expected entry.Complete to be true, got false")
	}
	if entry.PercentComplete != 1.0 {
		t.Errorf("expected entry.PercentComplete to be 1.0, got %f", entry.PercentComplete)
	}
	if m.activeToast == nil || !strings.Contains(m.activeToast.Message, "marked") || !strings.Contains(m.activeToast.Message, "complete") {
		t.Errorf("expected toast with 'marked ... complete', got %+v", m.activeToast)
	}

	// 2. Press 'm' again to unmark
	mdl, _ = m.updateEpisodes(runeKey("m"))
	m = mdl.(*modelImpl)

	_, ok = store.Get(key)
	if ok {
		t.Errorf("expected entry to be removed from history store after unmarking")
	}
	if m.activeToast == nil || !strings.Contains(m.activeToast.Message, "unmarked") {
		t.Errorf("expected toast with 'unmarked', got %+v", m.activeToast)
	}
}

func TestEpisodesSelectModeBatchMarkComplete(t *testing.T) {
	store, err := history.NewStore(filepath.Join(t.TempDir(), "history.json"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(store.Close)

	m := episodesTestModel()
	m.historyStore = store
	m.appMode = provider.ModeTV
	m.selectedSeries = &provider.SearchResult{Title: "The Boys", MediaType: provider.MediaTypeTV}
	m.seasonEpisodeIndex = 0

	// Enter select mode with space on episode 0
	mdl, _ := m.updateEpisodes(runeKey(" "))
	m = mdl.(*modelImpl)
	if !m.selectMode {
		t.Fatalf("expected selectMode to be true")
	}

	// Move down and toggle episode 1
	mdl, _ = m.updateEpisodes(runeKey("j"))
	m = mdl.(*modelImpl)
	mdl, _ = m.updateEpisodes(runeKey(" "))
	m = mdl.(*modelImpl)

	if len(m.selectedEpisodes) != 2 {
		t.Fatalf("expected 2 selected episodes, got %d", len(m.selectedEpisodes))
	}

	// Press 'm' to mark both complete
	mdl, _ = m.updateEpisodes(runeKey("m"))
	m = mdl.(*modelImpl)

	if m.selectMode {
		t.Errorf("selectMode should be false after batch mark complete")
	}
	if len(m.selectedEpisodes) != 0 {
		t.Errorf("selectedEpisodes should be empty after batch mark complete")
	}
	if m.activeToast == nil || !strings.Contains(m.activeToast.Message, "marked 2 episodes as complete") {
		t.Errorf("expected toast 'marked 2 episodes as complete', got %+v", m.activeToast)
	}

	for _, epNum := range []int{1, 2} {
		key := history.EntryKey{
			Title:     "The Boys",
			Mode:      string(provider.ModeTV),
			MediaType: provider.MediaTypeTV,
			Season:    1,
			Episode:   epNum,
		}
		entry, ok := store.Get(key)
		if !ok || !entry.Complete {
			t.Errorf("episode %d should be complete in store", epNum)
		}
	}

	// Re-enter select mode, select episode 0 and 1 (both complete)
	m.seasonEpisodeIndex = 0
	mdl, _ = m.updateEpisodes(runeKey(" "))
	m = mdl.(*modelImpl)
	mdl, _ = m.updateEpisodes(runeKey("j"))
	m = mdl.(*modelImpl)
	mdl, _ = m.updateEpisodes(runeKey(" "))
	m = mdl.(*modelImpl)

	// Press 'm': since all selected are complete, it unmarks both
	mdl, _ = m.updateEpisodes(runeKey("m"))
	m = mdl.(*modelImpl)

	if m.activeToast == nil || !strings.Contains(m.activeToast.Message, "unmarked 2 episodes") {
		t.Errorf("expected toast 'unmarked 2 episodes', got %+v", m.activeToast)
	}

	for _, epNum := range []int{1, 2} {
		key := history.EntryKey{
			Title:     "The Boys",
			Mode:      string(provider.ModeTV),
			MediaType: provider.MediaTypeTV,
			Season:    1,
			Episode:   epNum,
		}
		if _, ok := store.Get(key); ok {
			t.Errorf("episode %d should be deleted from store", epNum)
		}
	}
}

func TestDownloadsNavigationKeys(t *testing.T) {
	m := episodesTestModel()
	m.activeView = viewDownloads
	m.height = 20
	for i := 1; i <= 10; i++ {
		m.doneDownloads = append(m.doneDownloads, DoneDownload{
			Title: fmt.Sprintf("Ep %d", i),
			Size:  "100 MB",
		})
	}

	// 1. End / G jumps to bottom
	mdl, _ := m.updateDownloads(runeKey("G"))
	m = mdl.(*modelImpl)
	if m.downloadsIndex != 9 {
		t.Errorf("pressing 'G' should jump to last index (9), got %d", m.downloadsIndex)
	}

	// 2. Home / g jumps to top
	mdl, _ = m.updateDownloads(runeKey("g"))
	m = mdl.(*modelImpl)
	if m.downloadsIndex != 0 {
		t.Errorf("pressing 'g' should jump to index 0, got %d", m.downloadsIndex)
	}

	// 3. end key
	mdl, _ = m.updateDownloads(specialKey(tea.KeyEnd, "end"))
	m = mdl.(*modelImpl)
	if m.downloadsIndex != 9 {
		t.Errorf("pressing 'end' should jump to index 9, got %d", m.downloadsIndex)
	}

	// 4. home key
	mdl, _ = m.updateDownloads(specialKey(tea.KeyHome, "home"))
	m = mdl.(*modelImpl)
	if m.downloadsIndex != 0 {
		t.Errorf("pressing 'home' should jump to index 0, got %d", m.downloadsIndex)
	}

	// 5. j / down
	mdl, _ = m.updateDownloads(runeKey("j"))
	m = mdl.(*modelImpl)
	if m.downloadsIndex != 1 {
		t.Errorf("pressing 'j' should advance to index 1, got %d", m.downloadsIndex)
	}
	mdl, _ = m.updateDownloads(specialKey(tea.KeyDown, "down"))
	m = mdl.(*modelImpl)
	if m.downloadsIndex != 2 {
		t.Errorf("pressing 'down' should advance to index 2, got %d", m.downloadsIndex)
	}

	// 6. k / up
	mdl, _ = m.updateDownloads(runeKey("k"))
	m = mdl.(*modelImpl)
	if m.downloadsIndex != 1 {
		t.Errorf("pressing 'k' should return to index 1, got %d", m.downloadsIndex)
	}
	mdl, _ = m.updateDownloads(specialKey(tea.KeyUp, "up"))
	m = mdl.(*modelImpl)
	if m.downloadsIndex != 0 {
		t.Errorf("pressing 'up' should return to index 0, got %d", m.downloadsIndex)
	}

	// 7. ctrl+d jumps by pageStep
	mdl, _ = m.updateDownloads(specialKey(tea.KeyCtrlD, "ctrl+d"))
	m = mdl.(*modelImpl)
	if m.downloadsIndex != 9 {
		t.Errorf("ctrl+d should advance clamped to 9, got %d", m.downloadsIndex)
	}

	// 8. ctrl+u jumps up
	mdl, _ = m.updateDownloads(specialKey(tea.KeyCtrlU, "ctrl+u"))
	m = mdl.(*modelImpl)
	if m.downloadsIndex != 0 {
		t.Errorf("ctrl+u should return to 0, got %d", m.downloadsIndex)
	}

	// 9. pgdn / pgup
	mdl, _ = m.updateDownloads(specialKey(tea.KeyPgDown, "pgdown"))
	m = mdl.(*modelImpl)
	if m.downloadsIndex != 9 {
		t.Errorf("pgdn should advance clamped to 9, got %d", m.downloadsIndex)
	}
	mdl, _ = m.updateDownloads(specialKey(tea.KeyPgUp, "pgup"))
	m = mdl.(*modelImpl)
	if m.downloadsIndex != 0 {
		t.Errorf("pgup should return to 0, got %d", m.downloadsIndex)
	}
}
func TestDownloadsDirectPlayback(t *testing.T) {
	m := episodesTestModel()
	players := player.NewRegistry("", player.SkipClients{}, player.SkipSettings{})
	players.Register(&availableTestPlayer{name: "test-player"})
	m.players = players
	m.availablePlayers = players.AvailablePlayers()
	m.activeView = viewDownloads

	tmpFile := filepath.Join(t.TempDir(), "ep1.mp4")
	_ = os.WriteFile(tmpFile, []byte("video data"), 0o644)

	m.doneDownloads = []DoneDownload{
		{Title: "Loki S01E01", Path: tmpFile, Size: "1.2 GB"},
	}
	m.downloadsIndex = 0

	mdl, cmd := m.updateDownloads(specialKey(tea.KeyEnter, "enter"))
	m = mdl.(*modelImpl)
	if m.playOpID == 0 {
		t.Errorf("expected playOpID to be set on enter")
	}
	if cmd == nil {
		t.Errorf("expected cmd returned for direct playback")
	}
	if m.activeToast == nil || !strings.Contains(m.activeToast.Message, "launching playback") {
		t.Errorf("expected launching playback toast, got %+v", m.activeToast)
	}
}

func TestDownloadsDeleteCompletedFromDisk(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "Series", "Season 01")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	videoPath := filepath.Join(dir, "S01E01.mp4")
	subPath := filepath.Join(dir, "S01E01.en.srt")
	_ = os.WriteFile(videoPath, []byte("video"), 0o644)
	_ = os.WriteFile(subPath, []byte("sub"), 0o644)

	m := episodesTestModel()
	m.activeView = viewDownloads
	m.doneDownloads = []DoneDownload{
		{Title: "Series S01E01", Path: videoPath, Size: "500 MB"},
	}
	m.downloadsIndex = 0

	// First 'x' arms delete confirmation
	mdl, _ := m.updateDownloads(runeKey("x"))
	m = mdl.(*modelImpl)
	if !m.confirmStop {
		t.Fatalf("first x must set confirmStop")
	}
	if !strings.Contains(m.statusText, "delete file from disk") {
		t.Errorf("statusText = %q, want 'delete file from disk'", m.statusText)
	}

	// Second 'x' deletes the file and subtitle from disk
	mdl, _ = m.updateDownloads(runeKey("x"))
	m = mdl.(*modelImpl)
	if _, err := os.Stat(videoPath); !os.IsNotExist(err) {
		t.Errorf("videoPath should be deleted from disk")
	}
	if _, err := os.Stat(subPath); !os.IsNotExist(err) {
		t.Errorf("subPath should be deleted from disk")
	}
	if m.activeToast == nil || !strings.Contains(m.activeToast.Message, "file deleted from disk") {
		t.Errorf("toast = %+v, want 'file deleted from disk'", m.activeToast)
	}
	if len(m.doneDownloads) != 0 {
		t.Errorf("doneDownloads should be empty, got %v", m.doneDownloads)
	}
}

func TestRenderDownloadsScreen_Windowing(t *testing.T) {
	data := DownloadsData{
		SelectedIndex: 5,
		Width:         80,
		Height:        10,
		Accent:        lipgloss.AdaptiveColor{Light: "#000", Dark: "#fff"},
	}
	for i := 1; i <= 20; i++ {
		data.Done = append(data.Done, DoneDownload{
			Title: fmt.Sprintf("Show Episode %d", i),
			Size:  "200 MB",
		})
	}

	rendered := RenderDownloadsScreen(data)
	lines := strings.Split(rendered, "\n")
	if len(lines) > 10 {
		t.Errorf("rendered lines count = %d, expected <= 10 due to applyWindow", len(lines))
	}
	if !strings.Contains(rendered, "more") {
		t.Errorf("expected scroll hints in rendered screen with 20 items in height 10, got:\n%s", rendered)
	}
}

func TestActiveDownloadBadge_PausedPending(t *testing.T) {
	m := episodesTestModel()
	m.downloadPaused = true
	m.pendingDownload = &service.DownloadJob{
		Series: provider.SearchResult{Title: "Pending Show"},
	}
	st := NewStyles(m.currentAccent())
	badge := m.activeDownloadBadge(st)
	if !strings.Contains(badge, "paused download") {
		t.Errorf("expected 'paused download' badge, got %q", badge)
	}
}

func TestBatchDownloadQueuedEpisodeTitles(t *testing.T) {
	m := episodesTestModel()
	m.selectedSeries = &provider.SearchResult{Title: "The Boys"}
	m.batchInProgress = true
	m.batchCurrent = 1
	m.batchTotal = 2
	m.batchEpisodes = []provider.Episode{
		{Title: "Pilot", Season: 1, Episode: 1},
		{Title: "Cherry", Season: 1, Episode: 2},
	}

	data := m.collectDownloadsData(100, 30, m.currentAccent())
	if len(data.Active) != 2 {
		t.Fatalf("expected 2 active rows, got %d", len(data.Active))
	}
	if !strings.Contains(data.Active[0].Title, "Pilot") {
		t.Errorf("active row title = %q, expected to contain 'Pilot'", data.Active[0].Title)
	}
	if !strings.Contains(data.Active[1].Title, "Cherry") {
		t.Errorf("queued row title = %q, expected to contain 'Cherry'", data.Active[1].Title)
	}
}

func TestOnPlayDone_LocalPlayback(t *testing.T) {
	store, err := history.NewStore(filepath.Join(t.TempDir(), "history.json"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)

	m := episodesTestModel()
	m.historyStore = store
	m.activeView = viewDownloads
	m.playOpID = 42
	m.loading = true
	m.loadingText = "Opening player..."
	m.resolved = &model.ResolvedMedia{
		SeriesTitle: "Unrelated Show",
	}

	mdl, _ := m.onPlayDone(playDoneMsg{
		opID:     42,
		provider: "local",
		result:   player.PlaybackResult{FinalPositionSecs: 500, DurationSecs: 1000},
	})
	m = mdl.(*modelImpl)

	if m.activeView != viewDownloads {
		t.Errorf("activeView = %v, want viewDownloads after local playback", m.activeView)
	}
	if m.playOpID != 0 {
		t.Errorf("playOpID = %d, want 0", m.playOpID)
	}
	if m.loading {
		t.Errorf("loading must be false")
	}
	// Verify unrelated resolved media was NOT recorded in history
	key := history.EntryKey{Title: "Unrelated Show", Mode: string(m.appMode)}
	if _, found := store.Get(key); found {
		t.Errorf("local playback must not mutate history for unrelated resolved media")
	}
}

func TestCollectDownloadsData_PathCorrelation(t *testing.T) {
	root := t.TempDir()
	filePath := filepath.Join(root, "Loki S01E01.mp4")
	_ = os.WriteFile(filePath, []byte("video"), 0o644)

	svc := service.NewDownloadService(root, nil, nil, nil)

	m := episodesTestModel()
	m.downloadService = svc
	m.doneDownloads = []DoneDownload{
		{Title: "Loki S01E01", Size: "100 MB", Path: ""}, // in-memory session record with empty Path
	}

	data := m.collectDownloadsData(100, 30, m.currentAccent())
	if len(data.Done) != 1 {
		t.Fatalf("expected 1 done download, got %d", len(data.Done))
	}
	if data.Done[0].Path != filePath {
		t.Errorf("data.Done[0].Path = %q, want %q", data.Done[0].Path, filePath)
	}
}

func TestBatchDownloadResumeOffset(t *testing.T) {
	m := episodesTestModel()
	m.batchInProgress = true
	m.batchCurrent = 1
	m.batchTotal = 3
	m.batchEpisodes = []provider.Episode{
		{Title: "Ep 1", Season: 1, Episode: 1},
		{Title: "Ep 2", Season: 1, Episode: 2},
		{Title: "Ep 3", Season: 1, Episode: 3},
	}
	m.pendingDownload = &service.DownloadJob{
		Series:       provider.SearchResult{Title: "Show"},
		Episodes:     m.batchEpisodes,
		BatchCurrent: 2, // Resumed starting at episode 2
	}

	opID := m.newOpID()
	m.downloadOpID = opID

	// Simulate onBatchProgress with absolute index 2 (from batchDownloadCmd offset)
	mdl, _ := m.onBatchProgress(batchProgressMsg{
		opID:            opID,
		current:         2,
		total:           3,
		episodeTitle:    "S01E02",
		episodeProgress: 0.5,
	})
	m = mdl.(*modelImpl)

	if m.batchCurrent != 2 {
		t.Errorf("batchCurrent = %d, want 2", m.batchCurrent)
	}
	if m.pendingDownload.BatchCurrent != 2 {
		t.Errorf("pendingDownload.BatchCurrent = %d, want 2", m.pendingDownload.BatchCurrent)
	}

	data := m.collectDownloadsData(100, 30, m.currentAccent())
	if len(data.Active) != 2 { // Ep 2 (active) + Ep 3 (queued)
		t.Fatalf("expected 2 active/queued rows, got %d", len(data.Active))
	}
	if !strings.Contains(data.Active[0].Title, "Ep 2") {
		t.Errorf("active row should show resumed episode (Ep 2), got %q", data.Active[0].Title)
	}
}

func TestDownloadsHelpOverlay_Formatting(t *testing.T) {
	m := episodesTestModel()
	m.activeView = viewDownloads
	m.width = 100
	m.height = 30
	accent := m.currentAccent()
	out := m.renderHelpOverlayContent(m.width, accent)
	if strings.Contains(out, "/ open folder") {
		t.Errorf("found ugly broken line '/ open folder' in help overlay:\n%s", out)
	}
	if strings.Contains(out, "file from disk (press twice)") {
		t.Errorf("found ugly broken line 'file from disk (press twice)' in help overlay:\n%s", out)
	}
	for i, line := range strings.Split(out, "\n") {
		if lipgloss.Width(line) > m.width {
			t.Errorf("line %d width %d exceeds screen width %d: %q", i+1, lipgloss.Width(line), m.width, line)
		}
	}
}

func TestDownloadsFooter_OnDoneItem(t *testing.T) {
	m := episodesTestModel()
	m.activeView = viewDownloads
	m.doneDownloads = []DoneDownload{
		{Title: "One Piece S01E01", Size: "170 MB"},
	}
	m.downloadsIndex = 0 // On Done item
	bindings := m.activeKeyBindings()
	for _, b := range bindings {
		if strings.Contains(b.Action, "/") {
			t.Errorf("binding action %q contains ugly slash '/'", b.Action)
		}
	}
}

func TestDownloadsCancelActive_WithX(t *testing.T) {
	m := episodesTestModel()
	m.activeView = viewDownloads
	cancelled := false
	m.cancelDownload = func() { cancelled = true }
	m.downloadOpID = 123
	m.downloadTitle = "Loki S01E01"

	// First 'x' arms cancel confirmation
	mdl, _ := m.updateDownloads(runeKey("x"))
	m = mdl.(*modelImpl)
	if !m.confirmStop {
		t.Fatalf("first 'x' must set confirmStop")
	}
	if !strings.Contains(m.statusText, "cancel") {
		t.Errorf("statusText = %q, want cancel prompt", m.statusText)
	}

	// Second 'x' cancels download
	mdl, _ = m.updateDownloads(runeKey("x"))
	m = mdl.(*modelImpl)
	if !cancelled {
		t.Errorf("expected cancelDownload to be called")
	}
	if m.downloadOpID != 0 {
		t.Errorf("downloadOpID = %d, want 0", m.downloadOpID)
	}
}

func TestDownloadsCancelAll_WithX(t *testing.T) {
	m := episodesTestModel()
	m.activeView = viewDownloads
	batchCancelled := false
	m.batchInProgress = true
	m.batchCancel = func() { batchCancelled = true }

	// First 'X' arms confirmation
	mdl, _ := m.updateDownloads(runeKey("X"))
	m = mdl.(*modelImpl)
	if !m.confirmStop {
		t.Fatalf("first 'X' must set confirmStop")
	}

	// Second 'X' cancels all
	mdl, _ = m.updateDownloads(runeKey("X"))
	m = mdl.(*modelImpl)
	if !batchCancelled {
		t.Errorf("expected batchCancel to be called")
	}
	if m.batchInProgress {
		t.Errorf("batchInProgress should be false")
	}
}

func TestEpisodesFilter_CtrlF(t *testing.T) {
	m := episodesTestModel()
	m.activeView = viewEpisodes
	mdl, _ := m.updateEpisodes(specialKey(tea.KeyCtrlF, "ctrl+f"))
	m = mdl.(*modelImpl)
	if !m.episodeFiltering {
		t.Errorf("ctrl+f must trigger episodeFiltering")
	}
}
