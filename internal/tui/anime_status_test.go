package tui

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
	"kari/internal/history"
	"kari/internal/model"
	"kari/internal/provider"
	"kari/internal/scrobble"
)

func newTestStatusModel(t *testing.T) *modelImpl {
	t.Helper()
	storePath := filepath.Join(t.TempDir(), "history.json")
	store, err := history.NewStore(storePath)
	if err != nil {
		t.Fatalf("history.NewStore: %v", err)
	}
	t.Cleanup(func() {
		store.Close()
	})

	m := rankingTestModel()
	m.appCtx = context.Background()
	m.historyStore = store
	m.historyList = list.New([]list.Item{}, list.NewDefaultDelegate(), 80, 16)
	m.appMode = provider.ModeAnime
	m.activeView = viewPreview
	m.selectedSeries = &provider.SearchResult{
		ID:    "154587",
		Title: "Sousou no Frieren",
	}
	return m
}

func TestAnimeStatusModal_OpenOnPreviewAndEpisodes(t *testing.T) {
	m := newTestStatusModel(t)

	// 1. Open on preview
	mdl, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("t")})
	m = mdl.(*modelImpl)
	if !m.animeStatusPickerOpen {
		t.Fatal("expected animeStatusPickerOpen to be true on Preview")
	}
	if m.animeStatusTargetTitle != "Sousou no Frieren" {
		t.Errorf("targetTitle = %q, want 'Sousou no Frieren'", m.animeStatusTargetTitle)
	}
	if m.animeStatusTargetAniListID != 154587 {
		t.Errorf("targetAniListID = %d, want 154587", m.animeStatusTargetAniListID)
	}
	if !m.animeStatusLoading {
		t.Error("expected animeStatusLoading to be true")
	}
	if cmd == nil {
		t.Error("expected fetch command on modal open")
	}

	// Close with esc
	mdl, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = mdl.(*modelImpl)
	if m.animeStatusPickerOpen {
		t.Fatal("expected modal to close on Esc")
	}

	// 2. Open on episodes
	m.activeView = viewEpisodes
	mdl, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("t")})
	m = mdl.(*modelImpl)
	if !m.animeStatusPickerOpen {
		t.Fatal("expected animeStatusPickerOpen to be true on Episodes")
	}

	// 3. Non-anime mode does not open modal
	m.animeStatusPickerOpen = false
	m.appMode = provider.ModeTV
	mdl, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("t")})
	m = mdl.(*modelImpl)
	if m.animeStatusPickerOpen {
		t.Fatal("expected modal NOT to open when appMode is TV")
	}
}

func TestAnimeStatusModal_OpenOnHistory(t *testing.T) {
	m := newTestStatusModel(t)
	m.activeView = viewHistory

	// Add an anime entry in history
	entryKey := history.EntryKey{Title: "Frieren", Mode: "anime", MediaType: "tv", Season: 1, Episode: 1}
	_ = m.historyStore.Upsert(history.Entry{
		Key:       entryKey,
		Title:     "Frieren",
		Mode:      "anime",
		MediaType: "tv",
		Season:    1,
		Episode:   1,
		AniListID: 154587,
		Complete:  true,
		WatchedAt: time.Now(),
	})

	m.historyGroups = history.BuildGroups(m.historyStore.All())
	m.historyList = list.New([]list.Item{
		rowItem{title: "Frieren", key: m.historyGroups[0].Key.String()},
	}, list.NewDefaultDelegate(), 80, 20)
	m.historyList.Select(0)

	mdl, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("t")})
	m = mdl.(*modelImpl)
	if !m.animeStatusPickerOpen {
		t.Fatal("expected animeStatusPickerOpen to be true on History")
	}
	if m.animeStatusTargetTitle != "Frieren" {
		t.Errorf("targetTitle = %q, want 'Frieren'", m.animeStatusTargetTitle)
	}
	if m.animeStatusTargetAniListID != 154587 {
		t.Errorf("targetAniListID = %d, want 154587", m.animeStatusTargetAniListID)
	}
}

func TestAnimeStatusModal_NavigationAndDismiss(t *testing.T) {
	m := newTestStatusModel(t)
	m.animeStatusPickerOpen = true
	m.animeStatusPickerIndex = 0

	// Move down
	mdl, _ := m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = mdl.(*modelImpl)
	if m.animeStatusPickerIndex != 1 {
		t.Errorf("index = %d, want 1", m.animeStatusPickerIndex)
	}

	// Move up
	mdl, _ = m.Update(tea.KeyMsg{Type: tea.KeyUp})
	m = mdl.(*modelImpl)
	if m.animeStatusPickerIndex != 0 {
		t.Errorf("index = %d, want 0", m.animeStatusPickerIndex)
	}

	// Move up at 0 does not underflow
	mdl, _ = m.Update(tea.KeyMsg{Type: tea.KeyUp})
	m = mdl.(*modelImpl)
	if m.animeStatusPickerIndex != 0 {
		t.Errorf("index = %d, want 0", m.animeStatusPickerIndex)
	}

	// Dismiss
	mdl, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = mdl.(*modelImpl)
	if m.animeStatusPickerOpen {
		t.Error("expected picker to close on Esc")
	}
}

func TestAnimeStatusModal_RewatchAndResetActions(t *testing.T) {
	m := newTestStatusModel(t)

	// Populate history with 3 completed episodes
	grpKey := history.GroupKey{Title: "Frieren", Mode: "anime", MediaType: "tv"}
	for ep := 1; ep <= 3; ep++ {
		_ = m.historyStore.Upsert(history.Entry{
			Key:       history.EntryKey{Title: "Frieren", Mode: "anime", MediaType: "tv", Season: 1, Episode: ep},
			Title:     "Frieren",
			Mode:      "anime",
			MediaType: "tv",
			Season:    1,
			Episode:   ep,
			Complete:  true,
			WatchedAt: time.Now(),
		})
	}
	if len(m.historyStore.All()) != 3 {
		t.Fatalf("expected 3 entries in history, got %d", len(m.historyStore.All()))
	}

	// Select Rewatching action
	m.animeStatusPickerOpen = true
	m.animeStatusTargetTitle = "Frieren"
	m.animeStatusTargetKey = grpKey
	m.animeStatusPickerIndex = int(ActionAnimeRewatching)

	mdl, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = mdl.(*modelImpl)

	if m.animeStatusPickerOpen {
		t.Error("expected picker to close after selecting Rewatching")
	}
	if len(m.historyStore.All()) != 0 {
		t.Errorf("expected history entries to be cleared on Rewatching, got %d", len(m.historyStore.All()))
	}

	// Test Reset Local History action
	_ = m.historyStore.Upsert(history.Entry{
		Key:       history.EntryKey{Title: "Frieren", Mode: "anime", MediaType: "tv", Season: 1, Episode: 1},
		Title:     "Frieren",
		Mode:      "anime",
		MediaType: "tv",
		Season:    1,
		Episode:   1,
		Complete:  true,
		WatchedAt: time.Now(),
	})
	if len(m.historyStore.All()) != 1 {
		t.Fatalf("expected 1 entry in history, got %d", len(m.historyStore.All()))
	}

	m.animeStatusPickerOpen = true
	m.animeStatusTargetTitle = "Frieren"
	m.animeStatusTargetKey = grpKey
	m.animeStatusPickerIndex = int(ActionAnimeResetLocalHistory)

	mdl, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = mdl.(*modelImpl)

	if m.animeStatusPickerOpen {
		t.Error("expected picker to close after Reset Local History")
	}
	if len(m.historyStore.All()) != 0 {
		t.Errorf("expected history entries to be cleared, got %d", len(m.historyStore.All()))
	}
}

func TestAnimeStatusModal_LoadedMsg(t *testing.T) {
	m := newTestStatusModel(t)
	m.animeStatusPickerOpen = true
	m.animeStatusLoading = true
	m.animeStatusOpID = 42

	entry := &scrobble.MediaListEntry{
		ID:       123,
		Status:   scrobble.StatusCompleted,
		Progress: 28,
		Repeat:   2,
	}

	mdl, _ := m.Update(animeStatusLoadedMsg{
		opID:          42,
		entry:         entry,
		totalEpisodes: 28,
		err:           nil,
	})
	m = mdl.(*modelImpl)

	if m.animeStatusLoading {
		t.Error("expected loading to be false after loaded msg")
	}
	if m.animeStatusEntry == nil || m.animeStatusEntry.Status != scrobble.StatusCompleted {
		t.Errorf("unexpected animeStatusEntry: %+v", m.animeStatusEntry)
	}
	if m.animeStatusTotalEpisodes != 28 {
		t.Errorf("totalEpisodes = %d, want 28", m.animeStatusTotalEpisodes)
	}
}

func TestAnimeStatusOverlay_Render(t *testing.T) {
	accent := ResolveAccent(model.KindAnime, "Auto")
	st := NewStyles(accent)

	data := AnimeStatusOverlayData{
		Title:         "Sousou no Frieren",
		AniListID:     154587,
		Connected:     true,
		Loading:       false,
		CurrentEntry:  &scrobble.MediaListEntry{ID: 123, Status: scrobble.StatusRepeating, Progress: 3, Repeat: 1},
		TotalEpisodes: 28,
		SelectedIndex: 1, // Rewatching
		Accent:        st.Accent,
		Width:         80,
	}

	out := RenderAnimeStatusOverlay(data)
	if !strings.Contains(out, "Sousou no Frieren") {
		t.Errorf("rendered overlay missing title: %s", out)
	}
	if !strings.Contains(out, "REPEATING") {
		t.Errorf("rendered overlay missing current status: %s", out)
	}
	if !strings.Contains(out, "Rewatching") {
		t.Errorf("rendered overlay missing Rewatching option: %s", out)
	}
	if !strings.Contains(out, "enter select") {
		t.Errorf("rendered overlay missing footer hints: %s", out)
	}
}
