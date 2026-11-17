package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"

	"kari/internal/poster"
	"kari/internal/util"
)

func TestCalendarDetailsRemovesUpdatedLine(t *testing.T) {
	m := newTestCalendarModel(t)
	accent := lipgloss.AdaptiveColor{Light: "#7AA2F7", Dark: "#7AA2F7"}

	rendered := RenderCalendarScreen(m.collectCalendarData(120, 30, accent))
	if strings.Contains(rendered, "Updated") {
		t.Errorf("calendar detail pane should no longer show the update age, got:\n%s", rendered)
	}
}

func TestCalendarPosterSlotIsFixed(t *testing.T) {
	m := newTestCalendarModel(t)
	accent := lipgloss.AdaptiveColor{Light: "#7AA2F7", Dark: "#7AA2F7"}
	rows := calendarPosterRows(calendarDetailBodyHeight(30))
	marker := fixedBlock("POSTER_ART", calendarPosterMaxCols, rows)

	posterLine := func() int {
		t.Helper()
		data := m.collectCalendarData(120, 30, accent)
		data.PosterBlock = marker
		rendered := RenderCalendarScreen(data)
		for i, line := range strings.Split(rendered, "\n") {
			if strings.Contains(line, "POSTER_ART") {
				return i
			}
		}
		t.Fatalf("poster marker missing from render:\n%s", rendered)
		return -1
	}

	first := posterLine()
	m.calendarReleaseIndex = 1
	second := posterLine()
	if first != second {
		t.Errorf("poster moved when selection changed: line %d then %d", first, second)
	}
	if want := 2 + calendarDetailsMetaRows; first != want {
		t.Errorf("poster starts at line %d, want %d", first, want)
	}
}

func TestSyncCalendarPosterFollowsSelection(t *testing.T) {
	m := newTestCalendarModel(t)
	m.activeView = viewCalendar
	m.imagesEnabled = true
	m.posterClient = poster.NewClient(nil)
	m.posterCache = util.NewBoundedCache[string](30)

	cmd := m.syncCalendarPoster()
	if cmd == nil {
		t.Fatal("expected a poster fetch for the initial selection")
	}
	if m.calendarPosterKey != "frieren-8" {
		t.Fatalf("poster key = %q, want frieren-8", m.calendarPosterKey)
	}
	opID := m.calendarPosterOpID
	if cmd := m.syncCalendarPoster(); cmd != nil {
		t.Error("unrelated sync should not refetch the same release")
	}
	if m.calendarPosterOpID != opID {
		t.Errorf("opID changed from %d to %d without a selection change", opID, m.calendarPosterOpID)
	}

	m.calendarReleaseIndex = 1
	if cmd := m.syncCalendarPoster(); cmd == nil {
		t.Fatal("expected a poster fetch after moving the cursor")
	}
	if m.calendarPosterKey != "severance-s2e5" {
		t.Fatalf("poster key = %q, want severance-s2e5", m.calendarPosterKey)
	}
	if m.calendarPosterOpID == opID {
		t.Error("opID should bump so the stale fetch is discarded")
	}

	m.imagesEnabled = false
	m.calendarPosterKey = ""
	if cmd := m.syncCalendarPoster(); cmd != nil {
		t.Error("images disabled should not start a fetch")
	}
}

func TestCalendarPosterLoadedMsgRespectsOpID(t *testing.T) {
	m := newTestCalendarModel(t)
	m.calendarPosterOpID = 5

	m.Update(posterLoadedMsg{slot: posterSlotCalendar, opID: 5, rendered: "POSTER_ART"})
	if m.calendarPoster != "POSTER_ART" {
		t.Fatalf("calendar poster = %q, want POSTER_ART", m.calendarPoster)
	}

	m.Update(posterLoadedMsg{slot: posterSlotCalendar, opID: 4, rendered: "STALE"})
	if m.calendarPoster != "POSTER_ART" {
		t.Fatalf("stale opID overwrote the poster: %q", m.calendarPoster)
	}
}

func TestCalendarPosterVisibilityGates(t *testing.T) {
	m := newTestCalendarModel(t)
	m.activeView = viewCalendar
	m.imagesEnabled = true

	// 120 columns keeps the detail pane (and its cover) on screen.
	if !m.calendarPosterVisible() {
		t.Error("expected the calendar cover to be visible at 120 columns")
	}

	// Narrow terminals hide the whole detail pane.
	m.width = 80
	if m.calendarPosterVisible() {
		t.Error("calendar cover should be hidden when the detail pane is hidden")
	}

	// Too few rows to reserve a slot.
	m.width = 120
	m.height = 20
	if m.calendarPosterVisible() {
		t.Error("calendar cover should be hidden on short terminals")
	}
}
