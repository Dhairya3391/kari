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
