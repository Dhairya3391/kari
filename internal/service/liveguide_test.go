package service

import (
	"testing"
	"time"

	"kari/internal/provider"
)

func TestBuildLiveGuideSectionsAndCategories(t *testing.T) {
	now := time.Date(2026, time.June, 15, 20, 0, 0, 0, time.Local)
	results := []provider.SearchResult{
		{Title: "News", Group: "News", Live: true},
		{Title: "Sports", Group: "Sports", Live: true},
		{Title: "More news", Group: "News", Live: true},
		{Title: "Kickoff", Group: "Soccer", StartsAt: now.Add(30 * time.Minute)},
		{Title: "ESPN", Group: "Channel"},
		{Title: "CNN", Group: "Channel"},
	}

	guide := BuildLiveGuide(results, 1, now)
	if guide.Total != len(results) {
		t.Fatalf("Total = %d, want %d", guide.Total, len(results))
	}
	if guide.Counts.Now != 3 || guide.Counts.Upcoming != 1 || guide.Counts.Channels != 2 {
		t.Fatalf("Counts = %+v, want now=3 upcoming=1 channels=2", guide.Counts)
	}
	if len(guide.Sections) != 3 {
		t.Fatalf("Sections = %d, want 3", len(guide.Sections))
	}
	if got := guide.Sections[0]; got.Kind != LiveSectionNow || got.Start != 0 || got.Title != "Live now" {
		t.Errorf("first section = %+v", got)
	}
	if got := guide.Sections[2]; got.Kind != LiveSectionChannels || got.Start != 4 {
		t.Errorf("channels section = %+v", got)
	}
	if guide.SelectedRow < 0 || guide.Rows[guide.SelectedRow].Index != 1 {
		t.Fatalf("SelectedRow = %d, want the row for result 1", guide.SelectedRow)
	}

	var categories []string
	for _, row := range guide.Rows {
		if row.Kind == LiveRowCategory {
			categories = append(categories, row.Label)
			if row.Count < 1 {
				t.Errorf("category %q has count %d", row.Label, row.Count)
			}
		}
	}
	// Only the two-row channel run earns a subheading; lone events do not.
	want := []string{"Other channels"}
	if len(categories) != len(want) {
		t.Fatalf("categories = %v, want %v", categories, want)
	}
	for i := range want {
		if categories[i] != want[i] {
			t.Errorf("category %d = %q, want %q", i, categories[i], want[i])
		}
	}
}

func TestLiveTimeAndElapsedLabels(t *testing.T) {
	now := time.Date(2026, time.June, 15, 20, 0, 0, 0, time.Local)
	tests := []struct {
		name    string
		result  provider.SearchResult
		label   string
		elapsed string
	}{
		{
			name:   "channel",
			result: provider.SearchResult{Group: "Channel"},
			label:  "24/7",
		},
		{
			name:    "live event",
			result:  provider.SearchResult{Live: true, StartsAt: now.Add(-90 * time.Minute)},
			label:   "LIVE",
			elapsed: "1h30m",
		},
		{
			name:   "upcoming event",
			result: provider.SearchResult{StartsAt: now.Add(90 * time.Minute)},
			label:  "21:30",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := LiveTimeLabel(tt.result, now); got != tt.label {
				t.Errorf("label = %q, want %q", got, tt.label)
			}
			if got := LiveElapsedLabel(tt.result, now); got != tt.elapsed {
				t.Errorf("elapsed = %q, want %q", got, tt.elapsed)
			}
		})
	}
}

func TestLiveUpNext(t *testing.T) {
	now := time.Date(2026, time.June, 15, 20, 0, 0, 0, time.Local)
	results := []provider.SearchResult{
		{Title: "Now", Live: true},
		{Title: "Later", StartsAt: now.Add(time.Hour)},
	}
	stamp, title := LiveUpNext(results, 0, now)
	if stamp != "21:00" || title != "Later" {
		t.Fatalf("up next = %q %q, want 21:00 Later", stamp, title)
	}
	if stamp, title := LiveUpNext(results, 1, now); stamp != "" || title != "" {
		t.Fatalf("up next at end = %q %q, want empty", stamp, title)
	}
}
