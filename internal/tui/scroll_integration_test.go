package tui

// Regression tests for the whole-TUI scroll cleanup: list sizing must agree
// across layouts, and long lists must advertise that they continue.

import (
	"fmt"
	"strings"
	"testing"

	"kari/internal/calendar"
	"kari/internal/model"
	"kari/internal/provider"
)

func manyResults(n int) []provider.SearchResult {
	results := make([]provider.SearchResult, n)
	for i := range results {
		results[i] = provider.SearchResult{
			Title:     fmt.Sprintf("Title %02d", i),
			Year:      "2024",
			MediaType: provider.MediaTypeTV,
		}
	}
	return results
}

func countLinesWith(lines []string, needle string) int {
	n := 0
	for _, line := range lines {
		if strings.Contains(line, needle) {
			n++
		}
	}
	return n
}

func TestResultsPosterPaneUsesSameWindowAsSingleColumn(t *testing.T) {
	base := ResultsData{
		Mode:          provider.ModeTV,
		Results:       manyResults(100),
		SelectedIndex: 0,
		Height:        20,
		Accent:        ResolveAccent(model.KindTV, "Auto"),
	}
	single := base
	single.TermWidth = 60
	single.Width = 60
	pane := base
	pane.TermWidth = 120
	pane.Width = 120

	singleRows := countLinesWith(strings.Split(RenderResultsScreen(single), "\n"), "Title ")
	paneRows := countLinesWith(strings.Split(RenderResultsScreen(pane), "\n"), "Title ")
	if singleRows != paneRows {
		t.Fatalf("poster pane shows %d rows, single column shows %d: double reserve?", paneRows, singleRows)
	}
	if singleRows == 0 || !strings.Contains(RenderResultsScreen(single), "more") {
		t.Fatalf("long result list must show a scroll hint, rows=%d", singleRows)
	}
}

func TestEpisodesShowScrollHints(t *testing.T) {
	episodes := make([]provider.Episode, 60)
	for i := range episodes {
		episodes[i] = provider.Episode{Episode: i + 1, Title: fmt.Sprintf("Episode %d", i+1)}
	}
	data := EpisodesData{
		SeriesTitle:   "Show",
		Episodes:      episodes,
		SelectedIndex: 30,
		SeasonCount:   1,
		ActiveSeason:  1,
		Width:         100,
		Height:        20,
		Accent:        ResolveAccent(model.KindTV, "Auto"),
	}
	out := RenderEpisodesScreen(data)
	if !strings.Contains(out, "↑") || !strings.Contains(out, "↓") {
		t.Fatalf("long episode list must show both hints:\n%s", out)
	}
}

func TestCalendarListShowsScrollHints(t *testing.T) {
	accent := ResolveAccent(model.KindTV, "Auto")
	st := NewStyles(accent)
	items := make([]calendar.ReleaseItem, 50)
	for i := range items {
		items[i] = calendar.ReleaseItem{
			Release: calendar.Release{Title: fmt.Sprintf("Show %02d", i), Episode: i + 1},
		}
	}
	lines := renderCalendarList(CalendarData{SelectedIndex: 25}, items, 80, 10, st)
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "↑") || !strings.Contains(joined, "↓") {
		t.Fatalf("calendar list must show both hints in mid-list:\n%s", joined)
	}
}
