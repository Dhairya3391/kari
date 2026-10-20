package tui

// Reported row: "Golmaal Again  movies  0m/2h24m  ▰▱▱▱▱▱▱▱▱▱" — sub-minute
// progress rounded to 0m in text while the bar lit a cell.

import (
	"strings"
	"testing"
	"time"

	"kari/internal/history"
	"kari/internal/model"
)

func TestHistoryRowZeroProgressEmptyBar(t *testing.T) {
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
	if strings.Contains(out, "▰") {
		t.Errorf("zero-progress row must not light any cell, got:\n%s", out)
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
