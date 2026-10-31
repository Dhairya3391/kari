package tui

// History rendering stays compact while retaining the resume context users need.

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"kari/internal/history"
	"kari/internal/model"
	"kari/internal/provider"
	"kari/internal/termimg"
)

func TestHistoryShiftTabSwitchesSection(t *testing.T) {
	m := &modelImpl{
		keys:        defaultKeyMap(),
		historyList: list.New([]list.Item{}, list.NewDefaultDelegate(), 80, 16),
	}
	m.activeView = viewHistory
	m.updateHistory(tea.KeyMsg{Type: tea.KeyShiftTab})
	if m.historyTab != historyTabFinished {
		t.Fatalf("Shift+Tab selected history tab %d, want finished", m.historyTab)
	}
	m.updateHistory(tea.KeyMsg{Type: tea.KeyTab})
	if m.historyTab != historyTabContinue {
		t.Fatalf("Tab selected history tab %d, want continue", m.historyTab)
	}
}

func TestHistoryHorizontalArrowsDoNotPageTheList(t *testing.T) {
	items := make([]list.Item, 24)
	for i := range items {
		items[i] = rowItem{title: "Title", key: string(rune('a' + i)), index: i}
	}
	m := &modelImpl{
		keys:        defaultKeyMap(),
		historyList: list.New(items, list.NewDefaultDelegate(), 80, 5),
	}
	for _, key := range []struct {
		name  string
		type_ tea.KeyType
	}{{"left", tea.KeyLeft}, {"right", tea.KeyRight}} {
		m.updateHistory(tea.KeyMsg{Type: key.type_})
		if got := m.historySelectedIndex(); got != 0 {
			t.Fatalf("%s moved history selection to %d; want it to stay at 0", key.name, got)
		}
	}
}

func TestHistoryHorizontalArrowsCycleMode(t *testing.T) {
	m := &modelImpl{
		keys: defaultKeyMap(),
		historyGroups: []history.Group{
			{Title: "Frieren", Mode: string(provider.ModeAnime), HasIncomplete: true,
				ContinueEntry: history.Entry{Title: "Frieren", Mode: string(provider.ModeAnime), WatchedAt: time.Now()}},
			{Title: "The Bear", Mode: string(provider.ModeTV), HasIncomplete: true,
				ContinueEntry: history.Entry{Title: "The Bear", Mode: string(provider.ModeTV), WatchedAt: time.Now()}},
		},
		historyList: list.New([]list.Item{}, list.NewDefaultDelegate(), 80, 16),
	}
	m.refreshHistoryItems()
	m.updateHistory(tea.KeyMsg{Type: tea.KeyRight})
	if m.historyModeFilter != provider.ModeAnime {
		t.Fatalf("Right selected mode %q, want anime", m.historyModeFilter)
	}
	m.updateHistory(tea.KeyMsg{Type: tea.KeyLeft})
	if m.historyModeFilter != "" {
		t.Fatalf("Left selected mode %q, want all", m.historyModeFilter)
	}
}

func TestHistoryModeFiltersAndTextSearch(t *testing.T) {
	groups := []history.Group{
		{
			Title: "Frieren",
			Mode:  string(provider.ModeAnime),
			ContinueEntry: history.Entry{
				Title: "Frieren",
				Mode:  string(provider.ModeAnime),
			},
		},
		{
			Title: "The Bear",
			Mode:  string(provider.ModeTV),
			ContinueEntry: history.Entry{
				Title: "The Bear",
				Mode:  string(provider.ModeTV),
			},
		},
	}

	filters := historyModeFilters(groups, groups)
	if len(filters) != 3 || filters[0].Label != "All" || filters[1].Mode != provider.ModeAnime || filters[2].Mode != provider.ModeTV {
		t.Fatalf("unexpected mode filters: %#v", filters)
	}
	if filters[0].Count != 2 || filters[1].Count != 1 || filters[2].Count != 1 {
		t.Fatalf("unexpected mode counts: %#v", filters)
	}

	anime := filterHistoryGroups(groups, provider.ModeAnime)
	searched := searchHistoryGroups(anime, "fri")
	if len(searched) != 1 || searched[0].Title != "Frieren" {
		t.Fatalf("search returned %#v, want Frieren", searched)
	}
	if matches := searchHistoryGroups(groups, "not found"); len(matches) != 0 {
		t.Fatalf("search returned %#v, want no matches", matches)
	}
}

func TestHistoryNavigationUsesOneCompactModeSelector(t *testing.T) {
	data := HistoryData{
		ActiveTab:     HistoryTabContinue,
		ContinueCount: 30,
		FinishedCount: 61,
		ModeFilter:    provider.ModeAnime,
		ModeFilters: []HistoryModeFilter{
			{Label: "All", Count: 30},
			{Mode: provider.ModeAnime, Label: "Anime", Count: 10},
		},
		Width:  110,
		Accent: ResolveAccent(model.KindAnime, "Auto"),
	}
	navigation := renderHistoryNavigation(data)
	if strings.Contains(navigation, "\n") {
		t.Fatalf("wide navigation wrapped unexpectedly: %s", navigation)
	}
	if !strings.Contains(navigation, "Continue 30") || !strings.Contains(navigation, "Finished 61") || !strings.Contains(navigation, "Anime · 10") {
		t.Fatalf("navigation missing section or selected mode: %s", navigation)
	}
	if strings.Contains(navigation, "Cartoon") || strings.Contains(navigation, "Movies") {
		t.Fatalf("navigation should show one selected mode, got: %s", navigation)
	}
}

func TestHistoryEmptyStateReflectsActiveFilter(t *testing.T) {
	out := RenderHistoryScreen(HistoryData{
		ActiveTab:   HistoryTabContinue,
		ModeFilter:  provider.ModeAnime,
		FilterQuery: "frieren",
		Width:       80,
		Accent:      ResolveAccent(model.KindAnime, "Auto"),
	})
	if !strings.Contains(out, `No matches for "frieren" in anime history.`) {
		t.Fatalf("empty state does not describe the active filter:\n%s", out)
	}
}

func TestHistoryFilterPromptExplainsEditingAndResults(t *testing.T) {
	st := NewStyles(ResolveAccent(model.KindAnime, "Auto"))
	editing := renderHistoryFilterLine(HistoryData{Filtering: true}, 80, st)
	if !strings.Contains(editing, "type title, episode, or mode") || !strings.Contains(editing, "enter apply") || !strings.Contains(editing, "esc close") {
		t.Fatalf("editing prompt is missing filter guidance: %s", editing)
	}
	active := renderHistoryFilterLine(HistoryData{
		FilterQuery: "frieren",
		Groups:      make([]history.Group, 3),
	}, 80, st)
	if !strings.Contains(active, `"frieren"`) || !strings.Contains(active, "3 matches") || !strings.Contains(active, "/ edit") {
		t.Fatalf("active prompt is missing query results or edit hint: %s", active)
	}
}

func TestHistoryModeAndTextFilterUpdateRows(t *testing.T) {
	groups := []history.Group{
		{
			Title: "Frieren",
			Mode:  string(provider.ModeAnime),
			ContinueEntry: history.Entry{
				Title:     "Frieren",
				Mode:      string(provider.ModeAnime),
				WatchedAt: time.Now(),
			},
			HasIncomplete: true,
		},
		{
			Title: "The Bear",
			Mode:  string(provider.ModeTV),
			ContinueEntry: history.Entry{
				Title:     "The Bear",
				Mode:      string(provider.ModeTV),
				WatchedAt: time.Now(),
			},
			HasIncomplete: true,
		},
	}
	m := &modelImpl{
		activeView:    viewHistory,
		keys:          defaultKeyMap(),
		historyList:   list.New([]list.Item{}, list.NewDefaultDelegate(), 80, 16),
		historyGroups: groups,
	}

	m.updateHistory(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("]")})
	if m.historyModeFilter != provider.ModeAnime {
		t.Fatalf("mode filter = %q, want anime", m.historyModeFilter)
	}
	if got := len(m.historyList.Items()); got != 1 {
		t.Fatalf("mode filter produced %d rows, want 1", got)
	}

	m.historyFiltering = true
	m.updateHistory(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x")})
	if m.historyFilter != "x" || len(m.historyList.Items()) != 0 {
		t.Fatalf("text filter = %q with %d rows, want x with no rows", m.historyFilter, len(m.historyList.Items()))
	}
}

func TestHistoryFilterEditsExistingQueryAndClosesInStages(t *testing.T) {
	groups := []history.Group{{
		Title: "Frieren",
		Mode:  string(provider.ModeAnime),
		ContinueEntry: history.Entry{
			Title:     "Frieren",
			Mode:      string(provider.ModeAnime),
			WatchedAt: time.Now(),
		},
		HasIncomplete: true,
	}}
	m := &modelImpl{
		activeView:    viewHistory,
		keys:          defaultKeyMap(),
		historyList:   list.New([]list.Item{}, list.NewDefaultDelegate(), 80, 16),
		historyGroups: groups,
		historyFilter: "fri",
	}
	m.refreshHistoryItems()

	m.updateHistory(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("/")})
	if !m.historyFiltering || m.historyFilter != "fri" {
		t.Fatalf("reopening filter changed query or did not enter edit mode: %q, %v", m.historyFilter, m.historyFiltering)
	}
	m.updateHistory(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("e")})
	if m.historyFilter != "frie" || len(m.historyList.Items()) != 1 {
		t.Fatalf("filter edit = %q with %d rows, want frie with one row", m.historyFilter, len(m.historyList.Items()))
	}

	if !m.clearActiveFilter() || m.historyFiltering || m.historyFilter != "frie" {
		t.Fatalf("first Escape should close editing and keep the filter: %q, %v", m.historyFilter, m.historyFiltering)
	}
	if !m.clearActiveFilter() || m.historyFilter != "" || len(m.historyList.Items()) != 1 {
		t.Fatalf("second Escape should clear the filter: %q with %d rows", m.historyFilter, len(m.historyList.Items()))
	}
}

func TestHistoryFilterSupportsUnicodeBackspaceAndWordDelete(t *testing.T) {
	if got := removeLastRune("東京猫"); got != "東京" {
		t.Fatalf("removeLastRune returned %q, want 東京", got)
	}
	if got := removeLastWord("Frieren beyond"); got != "Frieren" {
		t.Fatalf("removeLastWord returned %q, want Frieren", got)
	}
	if got := removeLastWord("Frieren "); got != "" {
		t.Fatalf("removeLastWord with trailing space returned %q, want empty", got)
	}

	m := &modelImpl{
		keys:             defaultKeyMap(),
		historyList:      list.New([]list.Item{}, list.NewDefaultDelegate(), 80, 16),
		historyFilter:    "東京猫",
		historyFiltering: true,
	}
	m.updateHistory(tea.KeyMsg{Type: tea.KeyBackspace})
	if m.historyFilter != "東京" {
		t.Fatalf("backspace corrupted a multibyte query: %q", m.historyFilter)
	}
	m.updateHistory(tea.KeyMsg{Type: tea.KeyCtrlU})
	if m.historyFilter != "" {
		t.Fatalf("ctrl+u left query %q, want empty", m.historyFilter)
	}
}

func TestHistoryFilterIgnoresKittyImageAcknowledgement(t *testing.T) {
	m := &modelImpl{
		keys:             defaultKeyMap(),
		historyList:      list.New([]list.Item{}, list.NewDefaultDelegate(), 80, 16),
		historyFiltering: true,
	}
	for _, char := range "_Gi=4;OK\\" {
		m.updateHistory(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{char}})
	}
	if m.historyFilter != "" || m.historyTerminalInputPrefix != "" || m.historyDroppingTerminalResponse {
		t.Fatalf("Kitty acknowledgement affected filter input: query=%q prefix=%q dropping=%v", m.historyFilter, m.historyTerminalInputPrefix, m.historyDroppingTerminalResponse)
	}
}

func TestHistoryFilterKeepsJAndKAsSearchText(t *testing.T) {
	m := &modelImpl{
		keys:             defaultKeyMap(),
		historyList:      list.New([]list.Item{}, list.NewDefaultDelegate(), 80, 16),
		historyFiltering: true,
	}
	m.updateHistory(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("jk")})
	if m.historyFilter != "jk" {
		t.Fatalf("filter query = %q, want jk", m.historyFilter)
	}
}

func TestHistoryFilterAltBackspaceDeletesPreviousWord(t *testing.T) {
	m := &modelImpl{
		keys:             defaultKeyMap(),
		historyList:      list.New([]list.Item{}, list.NewDefaultDelegate(), 80, 16),
		historyFilter:    "Frieren beyond",
		historyFiltering: true,
	}
	m.updateHistory(tea.KeyMsg{Type: tea.KeyBackspace, Alt: true})
	if m.historyFilter != "Frieren" {
		t.Fatalf("Alt+Backspace left query %q, want Frieren", m.historyFilter)
	}
}

func TestHistoryKittyPosterUsesPersistentPlacementAfterFirstDraw(t *testing.T) {
	m := &modelImpl{
		historyGroups: []history.Group{{
			Title:         "Frieren",
			HasIncomplete: true,
			ContinueEntry: history.Entry{Title: "Frieren", WatchedAt: time.Now()},
		}},
		historyPoster:      "POSTER_ART",
		historyPosterDrawn: true,
		imagesEnabled:      true,
		imgProtocol:        termimg.ProtocolKitty,
	}
	data := m.collectHistoryData(120, 40, ResolveAccent(model.KindAnime, "Auto"))
	if strings.Contains(data.PosterBlock, "POSTER_ART") || !strings.Contains(data.PosterBlock, "\u200b") {
		t.Fatalf("redraw should reserve the existing Kitty placement without retransmitting art: %q", data.PosterBlock)
	}
}

func TestHistoryFilterDoesNotReloadPosterWhileSelectionStays(t *testing.T) {
	entry := history.Entry{
		Title:     "Frieren",
		Mode:      string(provider.ModeAnime),
		WatchedAt: time.Now(),
	}
	m := &modelImpl{
		keys:              defaultKeyMap(),
		historyList:       list.New([]list.Item{}, list.NewDefaultDelegate(), 80, 16),
		historyGroups:     []history.Group{{Title: entry.Title, Mode: entry.Mode, ContinueEntry: entry}},
		historyPoster:     "POSTER_ART",
		historyPosterOpID: 7,
		historyFiltering:  true,
	}
	m.refreshHistoryItems()

	m.updateHistory(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("F")})
	if m.historyPoster != "POSTER_ART" || m.historyPosterOpID != 7 {
		t.Fatalf("typing without changing selection reloaded artwork: %q, opID %d", m.historyPoster, m.historyPosterOpID)
	}
}

func TestHistoryRowZeroProgressPositionText(t *testing.T) {
	entry := history.Entry{
		Title:        "Golmaal Again",
		Mode:         "movies",
		MediaType:    "movie",
		PositionSecs: 30,
		DurationSecs: 2*3600 + 24*60,
		WatchedAt:    time.Now(),
	}
	data := HistoryData{
		ActiveTab:     HistoryTabContinue,
		ContinueCount: 1,
		Groups: []history.Group{
			{Title: entry.Title, Mode: "movies", ContinueEntry: entry},
		},
		SelectedIndex: 0,
		Width:         100,
		Height:        24,
		Accent:        ResolveAccent(model.KindMovie, "Auto"),
	}
	out := RenderHistoryScreen(data)
	if !strings.Contains(out, "0m/2h24m") {
		t.Fatalf("expected 0m/2h24m position text, got:\n%s", out)
	}
	if strings.Contains(out, "▰") || strings.Contains(out, "▱") {
		t.Errorf("history rows should not render a redundant progress bar, got:\n%s", out)
	}
}

// TestSortGroupsByDateBucketOrder verifies that sortGroupsByDateBucket places
// Today entries before This-week entries before Earlier entries, matching the
// section order renderHistoryList uses. Without this sort the historyList
// widget cursor (flat store order) and the renderer (bucket order) disagree,
// causing the wrong item to appear highlighted when scrolling through the
// history screen.
func TestSortGroupsByDateBucketOrder(t *testing.T) {
	now := time.Now()
	earlier := now.Add(-8 * 24 * time.Hour)
	thisWeek := now.Add(-2 * 24 * time.Hour)
	today := now.Add(-30 * time.Minute)

	makeGroup := func(title string, ts time.Time) history.Group {
		e := history.Entry{Title: title, WatchedAt: ts, PositionSecs: 10}
		return history.Group{Title: title, ContinueEntry: e, HasIncomplete: true}
	}

	// Store order: Earlier → This week → Today (intentionally reversed).
	groups := []history.Group{
		makeGroup("Earlier Show", earlier),
		makeGroup("This Week Show", thisWeek),
		makeGroup("Today Show", today),
	}

	sorted := sortGroupsByDateBucket(groups, false)
	if len(sorted) != 3 {
		t.Fatalf("expected 3 groups, got %d", len(sorted))
	}
	want := []string{"Today Show", "This Week Show", "Earlier Show"}
	for i, g := range sorted {
		if g.Title != want[i] {
			t.Errorf("sorted[%d] = %q, want %q", i, g.Title, want[i])
		}
	}

	// Original slice must be unmodified.
	if groups[0].Title != "Earlier Show" {
		t.Error("sortGroupsByDateBucket must not mutate the input slice")
	}
}

func TestBestHistorySeriesMatch_AniListID(t *testing.T) {
	results := []provider.SearchResult{
		{ID: "99999", Title: "Frieren: Beyond Journey's End Season 2"},
		{ID: "154587", Title: "Sousou no Frieren"},
	}

	entry := history.Entry{
		Title:     "Frieren",
		AniListID: 154587,
	}

	matched := bestHistorySeriesMatch(results, entry)
	if matched.ID != "154587" {
		t.Errorf("matched ID = %q, want 154587", matched.ID)
	}
}

func TestRenderHistoryRowIsCompactAndShowsProgressContext(t *testing.T) {
	st := NewStyles(ResolveAccent(model.KindAnime, "Auto"))
	entry := history.Entry{
		Title:        "Sousou no Frieren",
		EpisodeTitle: "The End of the Beginning",
		Mode:         string(provider.ModeAnime),
		MediaType:    provider.MediaTypeTV,
		Season:       1,
		Episode:      1,
		PositionSecs: 12 * 60,
		DurationSecs: 24 * 60,
		WatchedAt:    time.Now().Add(-5 * time.Minute),
	}
	group := history.Group{
		Title:         "Sousou no Frieren",
		Mode:          string(provider.ModeAnime),
		ContinueEntry: entry,
	}
	data := HistoryData{
		ActiveTab: HistoryTabContinue,
		Accent:    st.Accent,
	}
	out := renderHistoryRow(group, 0, true, data, 100, st)
	if !strings.Contains(out, "Sousou no Frieren") || !strings.Contains(out, "anime") || !strings.Contains(out, "s01e01 · 12m/24m") || !strings.Contains(out, "5m ago") {
		t.Errorf("row missing concise title and progress context: %s", out)
	}
	if lipgloss.Width(out) != 100 {
		t.Errorf("row width = %d, want aligned width 100: %s", lipgloss.Width(out), out)
	}
	if strings.Contains(out, "▰") || strings.Contains(out, "▱") {
		t.Errorf("row should not duplicate progress with a bar: %s", out)
	}
}

func TestHistoryRowMetadataColumnsAlignOnRight(t *testing.T) {
	entries := []string{
		historyRowMetadata("manga", "ch 1 · p1/57", "45m ago", 100),
		historyRowMetadata("movies", "13m/2h10m", "1h ago", 100),
		historyRowMetadata("live", "0m/0m", "2h ago", 100),
	}
	for _, metadata := range entries {
		if lipgloss.Width(metadata) != 42 {
			t.Errorf("metadata width = %d, want 42: %q", lipgloss.Width(metadata), metadata)
		}
	}
	modeEnd := func(metadata, mode string) int {
		return strings.Index(metadata, mode) + len(mode)
	}
	if modeEnd(entries[0], "manga") != 8 || modeEnd(entries[1], "movies") != 8 || modeEnd(entries[2], "live") != 8 {
		t.Fatalf("mode labels do not share a right edge: %#v", entries)
	}
	if !strings.HasSuffix(entries[0], "45m ago") || !strings.HasSuffix(entries[1], "1h ago") || !strings.HasSuffix(entries[2], "2h ago") {
		t.Fatalf("timestamps do not share the right edge: %#v", entries)
	}
}

func TestHistoryPosterPanelLabelsSelectedTitleBeforeArtwork(t *testing.T) {
	entry := history.Entry{
		Title:     "Frieren",
		Mode:      string(provider.ModeAnime),
		WatchedAt: time.Now(),
	}
	group := history.Group{
		Title:         entry.Title,
		Mode:          entry.Mode,
		ContinueEntry: entry,
		HasIncomplete: true,
	}
	out := RenderHistoryScreen(HistoryData{
		ActiveTab:     HistoryTabContinue,
		ContinueCount: 1,
		Groups:        []history.Group{group},
		SelectedIndex: 0,
		PosterBlock:   "POSTER_ART",
		Width:         110,
		Height:        24,
		Accent:        ResolveAccent(model.KindAnime, "Auto"),
	})
	titleAt := strings.Index(out, "Selected title")
	posterAt := strings.Index(out, "POSTER_ART")
	if titleAt < 0 || posterAt < 0 || titleAt > posterAt {
		t.Fatalf("poster panel should identify the selection before artwork:\n%s", out)
	}
	if strings.Contains(out, "│") {
		t.Fatalf("poster panel should use whitespace instead of a divider:\n%s", out)
	}
	for i, line := range strings.Split(out, "\n") {
		if width := lipgloss.Width(line); width > 110 {
			t.Errorf("line %d width = %d, exceeds history width 110: %q", i+1, width, line)
		}
		if strings.Contains(line, "Frieren") && lipgloss.Width(line) != 110 {
			t.Errorf("list row width = %d, want stable pane width 110: %q", lipgloss.Width(line), line)
		}
	}
	if lines := strings.Count(out, "\n") + 1; lines > 24 {
		t.Errorf("history height = %d, exceeds body height 24", lines)
	}
}
