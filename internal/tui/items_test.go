package tui

import (
	"strings"
	"testing"

	"kari/internal/history"
	"kari/internal/provider"
)

// TestHistoryTabs pins the tab split: in-progress (including untouched
// 0% rows) under Continue, completed-only under Finished.
func TestHistoryTabs(t *testing.T) {
	mkEntry := func(title string, pct float64, complete bool) history.Entry {
		return history.Entry{
			Key:             history.EntryKey{Title: title, Mode: "tv", MediaType: "tv"},
			Title:           title,
			PositionSecs:    pct * 100,
			DurationSecs:    100,
			PercentComplete: pct,
			Complete:        complete,
		}
	}
	groups := history.BuildGroups([]history.Entry{
		mkEntry("Done Show", 1.0, true),
		mkEntry("Mid Show", 0.45, false),
		mkEntry("Fresh Show", 0.0, false),
	})
	continued, finished := splitHistoryGroups(groups)
	if len(continued) != 2 || len(finished) != 1 {
		t.Fatalf("continued=%d finished=%d, want 2 and 1", len(continued), len(finished))
	}
	if finished[0].Title != "Done Show" {
		t.Errorf("finished = %q, want Done Show", finished[0].Title)
	}

	contItems := historyTabItems(continued, false)
	if len(contItems) != 2 {
		t.Fatalf("continue tab rows = %d, want 2 (flat, no headers)", len(contItems))
	}
	for _, it := range contItems {
		if row, ok := it.(rowItem); !ok || row.key == "" {
			t.Errorf("tab rows must be actionable: %+v", it)
		}
	}

}

// TestHistoryMarkers pins the marker scheme: checkmark for done,
// percent while partway, explicit 0% at zero — never blank, never ~.
func TestHistoryMarkers(t *testing.T) {
	cases := map[float64]string{
		1.0:  "[  ✓ ] ",
		0.45: "[ 45%] ",
		0.04: "[  4%] ",
		0.0:  "[  0%] ",
	}
	for pct, want := range cases {
		entry := history.Entry{PercentComplete: pct, Complete: pct >= 1}
		if got := progressMarker(entry); got != want {
			t.Errorf("pct %.2f marker = %q, want %q", pct, got, want)
		}
	}
}

// TestHistoryRowCopy pins status-only rows: resume point plus time,
// no counts, no repeated labels, explicit 0% for fresh rows.
func TestHistoryRowCopy(t *testing.T) {
	entry := history.Entry{
		Key:   history.EntryKey{Title: "T", Mode: "tv", MediaType: "tv"},
		Title: "T", PositionSecs: 0, DurationSecs: 100, PercentComplete: 0,
	}
	groups := history.BuildGroups([]history.Entry{entry})
	continued, _ := splitHistoryGroups(groups)
	items := historyTabItems(continued, false)
	if len(items) != 1 {
		t.Fatalf("got %d items, want 1 row", len(items))
	}
	row := items[0].(rowItem)
	if !strings.Contains(row.desc, "0%") {
		t.Errorf("fresh row desc = %q, want explicit 0%%", row.desc)
	}
	if strings.Contains(row.desc, "watched") || strings.Contains(row.desc, "Last played") {
		t.Errorf("row desc carries dropped metadata: %q", row.desc)
	}
}

func TestResultTypeLabelAndSeriesToItemsLive(t *testing.T) {
	liveItem := provider.SearchResult{
		Title:     "LIVE: Match 1",
		ID:        "pp-live:123",
		Type:      provider.ModeLive,
		MediaType: provider.MediaTypeLive,
		Year:      "2026-09-20 10:00",
	}
	if got := resultTypeLabel(liveItem); got != "Live" {
		t.Errorf("resultTypeLabel = %q, want Live", got)
	}
	if got := historyKindLabel(string(provider.ModeLive), provider.MediaTypeLive); got != "Live" {
		t.Errorf("historyKindLabel = %q, want Live", got)
	}
	items := seriesToItems([]provider.SearchResult{liveItem})
	if len(items) != 1 {
		t.Fatalf("seriesToItems len = %d, want 1", len(items))
	}
	row := items[0].(rowItem)
	if !strings.Contains(row.title, "[Live]") || !strings.Contains(row.title, "LIVE: Match 1") {
		t.Errorf("row title = %q, want [Live] badge and title", row.title)
	}
	if row.desc != "2026-09-20 10:00" {
		t.Errorf("row desc = %q, want 2026-09-20 10:00", row.desc)
	}
}
