package tui

// Reported row: "Golmaal Again  movies  0m/2h24m  ▰▱▱▱▱▱▱▱▱▱" — sub-minute
// progress rounded to 0m in text while the bar lit a cell.

import (
	"strings"
	"testing"
	"time"

	"kari/internal/history"
	"kari/internal/model"
	"kari/internal/provider"
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

func TestRenderHistoryDetails_EnrichedOutput(t *testing.T) {
	st := NewStyles(ResolveAccent(model.KindAnime, "Auto"))
	entry := history.Entry{
		Title:        "Sousou no Frieren",
		EpisodeTitle: "The End of the Beginning",
		Mode:         "anime",
		MediaType:    "tv",
		Season:       1,
		Episode:      1,
		PositionSecs: 720,
		DurationSecs: 1440,
		WatchedAt:    time.Now().Add(-5 * time.Minute),
	}
	group := history.Group{
		Title:         "Sousou no Frieren",
		Mode:          "anime",
		ContinueEntry: entry,
		WatchedCount:  4,
	}

	data := HistoryData{
		Groups:        []history.Group{group},
		SelectedIndex: 0,
		Width:         80,
		Height:        24,
		Accent:        st.Accent,
	}

	out := renderHistoryDetails(data, 40, st)
	if !strings.Contains(out, "The End of the Beginning") {
		t.Errorf("details missing episode title: %s", out)
	}
	if !strings.Contains(out, "12:00 / 24:00 (50%)") {
		t.Errorf("details missing timestamp: %s", out)
	}
	if !strings.Contains(out, "4 watched") {
		t.Errorf("details missing watched count: %s", out)
	}
	if !strings.Contains(out, "status / rewatch") {
		t.Errorf("details missing status shortcut hint: %s", out)
	}
}
