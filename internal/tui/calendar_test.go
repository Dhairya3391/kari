package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"kari/internal/calendar"
	"kari/internal/history"
	"kari/internal/provider"
)

func newTestCalendarModel() *modelImpl {
	ti := textinput.New()
	ti.Blur()

	now := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	weekStart := calendar.StartOfWeek(now)

	m := &modelImpl{
		appCtx:               context.Background(),
		activeView:           viewSearch,
		queryInput:           ti,
		keys:                 defaultKeyMap(),
		calendarReleaseIndex: 0,
		calendarLoaded:       true,
		width:                120,
		height:               30,
	}

	// Create test schedule
	releases := []calendar.Release{
		{
			ID:        "frieren-8",
			Title:     "Sousou no Frieren",
			Mode:      provider.ModeAnime,
			Episode:   8,
			AniListID: 154587,
			AiringAt:  now,
		},
		{
			ID:       "severance-s2e5",
			Title:    "Severance",
			Mode:     provider.ModeTV,
			Season:   2,
			Episode:  5,
			TMDBID:   95396,
			AiringAt: now.Add(2 * time.Hour),
		},
		{
			ID:       "solo-12",
			Title:    "Solo Leveling",
			Mode:     provider.ModeAnime,
			Episode:  12,
			AiringAt: now.Add(4 * time.Hour),
		},
	}

	histPath := filepathJoin(tTempDir(), "history.json")
	store, _ := history.NewStore(histPath)
	_ = store.Upsert(history.Entry{
		Key: history.EntryKey{
			Title:     "Sousou no Frieren",
			Mode:      "anime",
			MediaType: "anime",
			Season:    1,
			Episode:   5,
		},
		Title:           "Sousou no Frieren",
		Season:          1,
		Episode:         5,
		PercentComplete: 1.0,
		Complete:        true,
		AniListID:       154587,
	})

	m.historyStore = store
	m.calendarSchedule = calendar.BuildWeeklySchedule(releases, store, weekStart, now)
	for i, day := range m.calendarSchedule.Days {
		if day.IsToday {
			m.calendarDayIndex = i
			break
		}
	}
	return m
}

func filepathJoin(elem ...string) string {
	return strings.Join(elem, "/")
}

var tempDirCounter int

func tTempDir() string {
	tempDirCounter++
	return "/tmp/kari-test-" + time.Now().Format("20060102150405")
}

func TestRenderCalendarScreen(t *testing.T) {
	m := newTestCalendarModel()
	accent := lipgloss.AdaptiveColor{Light: "#7AA2F7", Dark: "#7AA2F7"}
	data := m.collectCalendarData(120, 30, accent)

	rendered := RenderCalendarScreen(data)
	if !strings.Contains(rendered, "Sousou no Frieren") {
		t.Errorf("expected calendar screen to render 'Sousou no Frieren', got:\n%s", rendered)
	}
	if !strings.Contains(rendered, "Left: Ep 6-8 (3 left)") {
		t.Errorf("expected left-to-watch badge 'Left: Ep 6-8 (3 left)', got:\n%s", rendered)
	}
	if !strings.Contains(rendered, "Severance") {
		t.Errorf("expected calendar screen to render 'Severance', got:\n%s", rendered)
	}
	if !strings.Contains(rendered, "WATCH PROGRESS") {
		t.Errorf("expected right detail panel to render 'WATCH PROGRESS', got:\n%s", rendered)
	}
}

func TestCalendar_Navigation(t *testing.T) {
	m := newTestCalendarModel()

	// 1. Open Calendar
	cmd := m.openCalendar()
	if m.activeView != viewCalendar {
		t.Fatalf("activeView = %v, want viewCalendar", m.activeView)
	}
	_ = cmd

	// 2. Down key moves cursor
	mdl, _ := m.updateCalendar(tea.KeyMsg{Type: tea.KeyDown})
	m = mdl.(*modelImpl)
	if m.calendarReleaseIndex != 1 {
		t.Errorf("calendarReleaseIndex = %d, want 1", m.calendarReleaseIndex)
	}

	// 3. Up key moves cursor back
	mdl, _ = m.updateCalendar(tea.KeyMsg{Type: tea.KeyUp})
	m = mdl.(*modelImpl)
	if m.calendarReleaseIndex != 0 {
		t.Errorf("calendarReleaseIndex = %d, want 0", m.calendarReleaseIndex)
	}

	// 4. Right key changes day
	origDay := m.calendarDayIndex
	mdl, _ = m.updateCalendar(tea.KeyMsg{Type: tea.KeyRight})
	m = mdl.(*modelImpl)
	if m.calendarDayIndex != (origDay+1)%7 {
		t.Errorf("calendarDayIndex = %d, want %d", m.calendarDayIndex, (origDay+1)%7)
	}

	// 5. Left key changes day back
	mdl, _ = m.updateCalendar(tea.KeyMsg{Type: tea.KeyLeft})
	m = mdl.(*modelImpl)
	if m.calendarDayIndex != origDay {
		t.Errorf("calendarDayIndex = %d, want %d", m.calendarDayIndex, origDay)
	}

	// 6. Tab cycles filter
	mdl, _ = m.updateCalendar(tea.KeyMsg{Type: tea.KeyTab})
	m = mdl.(*modelImpl)
	if m.calendarFilter != calendar.FilterAnime {
		t.Errorf("calendarFilter = %v, want FilterAnime", m.calendarFilter)
	}

	// 7. '4' jumps to FilterWatching
	mdl, _ = m.updateCalendar(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'4'}})
	m = mdl.(*modelImpl)
	if m.calendarFilter != calendar.FilterWatching {
		t.Errorf("calendarFilter = %v, want FilterWatching", m.calendarFilter)
	}

	// 8. '1' jumps back to FilterAll
	mdl, _ = m.updateCalendar(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'1'}})
	m = mdl.(*modelImpl)
	if m.calendarFilter != calendar.FilterAll {
		t.Errorf("calendarFilter = %v, want FilterAll", m.calendarFilter)
	}
}

func TestCalendar_GlobalKeyToggle(t *testing.T) {
	m := newTestCalendarModel()
	m.activeView = viewSearch

	// Pressing 'c' on search view opens calendar
	cmd, handled := m.handleGlobalKeys(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'c'}})
	if !handled || m.activeView != viewCalendar {
		t.Errorf("activeView = %v, want viewCalendar after pressing 'c'", m.activeView)
	}
	_ = cmd

	// Pressing 'esc' goes back to search view
	m.updateCalendar(tea.KeyMsg{Type: tea.KeyEscape})
	if m.activeView != viewSearch {
		t.Errorf("activeView = %v, want viewSearch after pressing 'esc'", m.activeView)
	}
}
func TestCalendar_EnterResumesWatchedShow(t *testing.T) {
	m := newTestCalendarModel()
	m.activeView = viewCalendar
	m.calendarReleaseIndex = 0 // Frieren (watched up to ep 5, airing 8)

	// Pressing enter should trigger resumeHistoryEntry for next unwatched ep (ep 6)
	mdl, cmd := m.updateCalendar(tea.KeyMsg{Type: tea.KeyEnter})
	m = mdl.(*modelImpl)

	if !m.loading {
		t.Error("expected loading state after pressing enter on in-progress show")
	}
	if !strings.Contains(m.loadingText, "Finding Sousou no Frieren") {
		t.Errorf("loadingText = %q, want finding Frieren", m.loadingText)
	}
	if cmd == nil {
		t.Error("expected resume cmd")
	}
}

func TestCalendar_EscapeFilterBehavior(t *testing.T) {
	m := newTestCalendarModel()
	m.pushView(viewCalendar)
	m.calendarFiltering = true
	m.calendarQuery = "frieren"

	// 1. First Escape should close filter typing mode, but stay on Calendar and keep query
	cmd, handled := m.handleGlobalKeys(tea.KeyMsg{Type: tea.KeyEscape})
	if !handled || cmd != nil {
		t.Fatal("expected escape to be handled")
	}
	if m.activeView != viewCalendar {
		t.Errorf("activeView = %v, want viewCalendar after first escape", m.activeView)
	}
	if m.calendarFiltering {
		t.Error("expected calendarFiltering to be false after first escape")
	}
	if m.calendarQuery != "frieren" {
		t.Errorf("calendarQuery = %q, want 'frieren'", m.calendarQuery)
	}

	// 2. Second Escape should clear the query, still staying on Calendar
	cmd, handled = m.handleGlobalKeys(tea.KeyMsg{Type: tea.KeyEscape})
	if !handled || cmd != nil {
		t.Fatal("expected second escape to be handled")
	}
	if m.activeView != viewCalendar {
		t.Errorf("activeView = %v, want viewCalendar after second escape", m.activeView)
	}
	if m.calendarQuery != "" {
		t.Errorf("expected calendarQuery to be empty, got %q", m.calendarQuery)
	}

	// 3. Third Escape with no filter goes back to previous view (Search)
	cmd, handled = m.handleGlobalKeys(tea.KeyMsg{Type: tea.KeyEscape})
	if !handled || cmd != nil {
		t.Fatal("expected third escape to be handled")
	}
	if m.activeView != viewSearch {
		t.Errorf("activeView = %v, want viewSearch after third escape", m.activeView)
	}
}

func TestCalendar_NoDuplicateEpisodeLabel(t *testing.T) {
	accent := lipgloss.AdaptiveColor{Light: "#7AA2F7", Dark: "#7AA2F7"}
	st := NewStyles(accent)

	// Unstarted TV show
	unstarted := calendar.ReleaseItem{
		Release: calendar.Release{
			Title:   "Beyond the Gates",
			Mode:    provider.ModeTV,
			Season:  2,
			Episode: 204,
		},
		Status:    calendar.StatusNotStarted,
		InHistory: false,
	}

	row := renderCalendarRow(unstarted, false, 100, st)
	count := strings.Count(row, "S02E204")
	if count != 1 {
		t.Errorf("expected 'S02E204' to appear exactly once, got %d occurrences in:\n%s", count, row)
	}
}

func TestCalendar_CenteredScroll(t *testing.T) {
	accent := lipgloss.AdaptiveColor{Light: "#7AA2F7", Dark: "#7AA2F7"}
	st := NewStyles(accent)

	items := make([]calendar.ReleaseItem, 50)
	for i := range 50 {
		items[i] = calendar.ReleaseItem{
			Release: calendar.Release{
				Title:   strings.Repeat("Show", 1) + string(rune('A'+i%26)),
				Episode: i + 1,
			},
		}
	}

	// Viewport height 10, cursor at 25
	data := CalendarData{
		SelectedIndex: 25,
	}

	lines := renderCalendarList(data, items, 80, 10, st)
	if len(lines) != 10 {
		t.Fatalf("expected 10 rendered lines, got %d", len(lines))
	}

	// Selected line (index 25) should be in the vertical middle (approx line 5 of 10)
	foundSelectedAt := -1
	for idx, line := range lines {
		if strings.Contains(line, "▌") {
			foundSelectedAt = idx
			break
		}
	}

	if foundSelectedAt != 5 {
		t.Errorf("expected selected cursor at vertical center (line 5), got line %d", foundSelectedAt)
	}
}
