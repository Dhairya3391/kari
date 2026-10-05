package tui

// The live/results/history lists rendered every row, so on long lists the
// cursor walked off-screen while the viewport stayed put. These pin the
// windowing contract: the selected row is always visible, clipped rows
// collapse into ↑/↓ hints, short lists pass through, and the window stays
// put while the cursor moves through its middle (no per-keypress jumps).

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"kari/internal/history"
	"kari/internal/model"
	"kari/internal/provider"
)

func TestWindowRows(t *testing.T) {
	rows := []string{"a", "b", "c", "d", "e", "f", "g"}

	// Short list passes through.
	out, above, below := windowRows(rows[:3], 0, 5)
	if len(out) != 3 || above != 0 || below != 0 {
		t.Errorf("short list should pass through: %v %d %d", out, above, below)
	}
	// Cursor at top: head window.
	out, above, below = windowRows(rows, 0, 5)
	if len(out) != 5 || above != 0 || below != 2 {
		t.Errorf("top window wrong: %v %d %d", out, above, below)
	}
	// Cursor at bottom: tail window, cursor visible.
	out, above, below = windowRows(rows, 6, 5)
	if len(out) != 5 || above != 2 || below != 0 || out[4] != "g" {
		t.Errorf("bottom window wrong: %v %d %d", out, above, below)
	}
	// Cursor clamped into range.
	out, _, _ = windowRows(rows, 99, 5)
	if out[4] != "g" {
		t.Errorf("clamped cursor should show tail: %v", out)
	}
}

// The window slides smoothly: a one-row cursor step shifts the window by
// at most one row, and the cursor always stays visible. The centered rule
// keeps the cursor mid-screen instead of pinned to the bottom edge.
func TestWindowRowsStableWhileCursorMidList(t *testing.T) {
	rows := []string{"a", "b", "c", "d", "e", "f", "g", "h", "i", "j"}

	for cursor := 1; cursor < len(rows)-1; cursor++ {
		_, above1, below1 := windowRows(rows, cursor-1, 5)
		out, above, below := windowRows(rows, cursor, 5)
		// Smooth: at most one row of drift per one-row cursor step.
		if abs(above-above1) > 1 || abs(below-below1) > 1 {
			t.Errorf("cursor %d: window jumped (%d/%d → %d/%d)", cursor, above1, below1, above, below)
		}
		// Cursor row visible inside the window (above == window start).
		if pos := cursor - above; pos < 0 || pos >= len(out) || out[pos] != rows[cursor] {
			t.Errorf("cursor %d not visible in window %v (start %d)", cursor, out, above)
		}
	}

	// Edges: head and tail windows pin correctly.
	out, above, below := windowRows(rows, 0, 5)
	if out[0] != "a" || above != 0 || below != 5 {
		t.Errorf("head window wrong: %v %d %d", out, above, below)
	}
	out, above, below = windowRows(rows, 9, 5)
	if out[4] != "j" || above != 5 || below != 0 {
		t.Errorf("tail window wrong: %v %d %d", out, above, below)
	}
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

func liveFixtureTitles(n int) []provider.SearchResult {
	out := make([]provider.SearchResult, n)
	for i := range out {
		out[i] = provider.SearchResult{
			Title:     fmt.Sprintf("Match %02d", i+1),
			Group:     "Soccer",
			MediaType: provider.MediaTypeLive,
			Live:      true,
		}
	}
	return out
}

// Live grouping must come only from Live/StartsAt/Group: schedule text in
// Year is ignored, and 24/7 channels sort under Channels even without a
// schedule.
func TestLiveClassificationIgnoresYear(t *testing.T) {
	now := time.Now()
	// A same-day future fixture: +3h, pulled back when that would
	// cross midnight (the old fixed +3h failed every evening east of
	// UTC). Truncated to the minute like real schedule payloads.
	deltaMin := 180
	if minsIn := now.Hour()*60 + now.Minute(); minsIn+deltaMin >= 24*60 {
		deltaMin = (24*60 - minsIn) / 2
	}
	if deltaMin < 5 {
		t.Skip("too close to midnight for a same-day fixture")
	}
	later := now.Add(time.Duration(deltaMin) * time.Minute)
	event := time.Date(now.Year(), now.Month(), now.Day(), later.Hour(), later.Minute(), 0, 0, time.Local)
	tmrw := event.AddDate(0, 0, 1)

	tests := []struct {
		name       string
		r          provider.SearchResult
		wantGroup  liveGroup
		wantHeader string
	}{
		{
			// Regression: pengu used to stuff "Live" into Year; grouping
			// must not depend on it.
			name:      "year text never marks live",
			r:         provider.SearchResult{Title: "Match", Year: "Live"},
			wantGroup: liveGroupLater,
		},
		{
			name:      "explicit live wins",
			r:         provider.SearchResult{Title: "Match", Live: true},
			wantGroup: liveGroupNow,
		},
		{
			name:      "channel group overrides schedule",
			r:         provider.SearchResult{Title: "ESPN", Group: "Channel", Live: true},
			wantGroup: liveGroupChannels,
		},
		{
			name:      "future start today",
			r:         provider.SearchResult{Title: "Match", StartsAt: event},
			wantGroup: liveGroupToday,
		},
		{
			name:      "future start tomorrow",
			r:         provider.SearchResult{Title: "Match", StartsAt: tmrw},
			wantGroup: liveGroupTomorrow,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g, _ := classifyLiveResult(tt.r)
			if g != tt.wantGroup {
				t.Errorf("group = %v, want %v", g, tt.wantGroup)
			}
			if name := liveGroupName[g]; name != tt.wantHeader && tt.wantHeader != "" {
				t.Errorf("header = %q, want %q", name, tt.wantHeader)
			}
		})
	}
}

func TestLiveResultsScrollWithCursor(t *testing.T) {
	data := ResultsData{
		Query:         "search live…",
		Mode:          provider.ModeLive,
		Results:       liveFixtureTitles(40),
		SelectedIndex: 35,
		Width:         100,
		Height:        24,
		Accent:        ResolveAccent(model.KindLive, "Auto"),
	}
	out := RenderLiveResultsScreen(data)
	if !strings.Contains(out, "Match 36") {
		t.Errorf("selected row must be visible:\n%s", out)
	}
	if strings.Contains(out, "Match 01") {
		t.Errorf("head rows must scroll away:\n%s", out)
	}
	if !strings.Contains(out, "↑") {
		t.Errorf("expected scroll-up hint:\n%s", out)
	}
}

func TestResultsScrollWithCursor(t *testing.T) {
	var results []provider.SearchResult
	for i := 1; i <= 30; i++ {
		results = append(results, provider.SearchResult{
			Title:     fmt.Sprintf("Title %02d", i),
			Year:      "2024",
			MediaType: provider.MediaTypeTV,
		})
	}
	data := ResultsData{
		Query:         "boys",
		Mode:          provider.ModeTV,
		Results:       results,
		SelectedIndex: 25,
		Width:         100,
		Height:        24,
		Accent:        ResolveAccent(model.KindTV, "Auto"),
	}
	out := RenderResultsScreen(data)
	if !strings.Contains(out, "Title 26") {
		t.Errorf("selected row must be visible:\n%s", out)
	}
	if strings.Contains(out, "Title 01") {
		t.Errorf("head rows must scroll away:\n%s", out)
	}
	if !strings.Contains(out, "↑") {
		t.Errorf("expected scroll-up hint:\n%s", out)
	}
}

func TestHistoryScrollWithCursor(t *testing.T) {
	var groups []history.Group
	now := time.Now()
	for i := 1; i <= 20; i++ {
		entry := history.Entry{
			Title:     fmt.Sprintf("Show %02d", i),
			Mode:      "tv",
			MediaType: "tv",
			WatchedAt: now.Add(-time.Duration(i*13) * time.Hour),
		}
		groups = append(groups, history.Group{
			Title:         entry.Title,
			Mode:          "tv",
			ContinueEntry: entry,
		})
	}
	data := HistoryData{
		ActiveTab:     HistoryTabContinue,
		ContinueCount: 20,
		Groups:        groups,
		SelectedIndex: 15,
		Width:         80,
		Height:        16,
		Accent:        ResolveAccent(model.KindAnime, "Auto"),
	}
	out := RenderHistoryScreen(data)
	if !strings.Contains(out, "Show 16") {
		t.Errorf("selected row must be visible:\n%s", out)
	}
	if strings.Contains(out, "Show 01") {
		t.Errorf("head rows must scroll away:\n%s", out)
	}
}
func TestApplyWindowNeverExceedsHeight(t *testing.T) {
	st := NewStyles(ResolveAccent(model.KindAnime, "Auto"))
	rows := make([]string, 50)
	for i := range rows {
		rows[i] = fmt.Sprintf("row %02d", i)
	}

	for height := 6; height <= 30; height++ {
		for headerReserve := 0; headerReserve <= 4; headerReserve++ {
			avail := max(3, height-headerReserve)
			for cursor := range rows {
				out := applyWindow(rows, cursor, height, headerReserve, st)
				if len(out) > avail {
					t.Fatalf("height=%d reserve=%d cursor=%d: len(out)=%d exceeds avail=%d", height, headerReserve, cursor, len(out), avail)
				}
			}
		}
	}
}

func TestHistoryScreenFitsBodyHeight(t *testing.T) {
	var groups []history.Group
	now := time.Now()
	for i := 1; i <= 55; i++ {
		entry := history.Entry{
			Title:     fmt.Sprintf("Title %02d", i),
			Mode:      "anime",
			MediaType: "anime",
			WatchedAt: now.Add(-time.Duration(i) * time.Hour),
		}
		groups = append(groups, history.Group{
			Title:         entry.Title,
			Mode:          "anime",
			ContinueEntry: entry,
		})
	}

	data := HistoryData{
		ActiveTab:     HistoryTabContinue,
		ContinueCount: 55,
		Groups:        groups,
		SelectedIndex: 25,
		Width:         100,
		Height:        24,
		Accent:        ResolveAccent(model.KindAnime, "Auto"),
	}

	out := RenderHistoryScreen(data)
	lines := strings.Split(out, "\n")
	if len(lines) > data.Height {
		t.Fatalf("rendered history lines=%d exceeds data.Height=%d", len(lines), data.Height)
	}
	if !strings.Contains(out, "↑") || !strings.Contains(out, "↓") {
		t.Fatalf("expected both scroll hints in middle of 55 items, got:\n%s", out)
	}
}

// Narrow screens stay centered on wide terminals: centerBlock adds a left
// gutter around the whole block without touching inner alignment, and
// leaves wide content byte-identical.

func TestCenterBlockCentersNarrowContent(t *testing.T) {
	out := centerBlock("ab\ncdef", 20, 76)
	lines := strings.Split(out, "\n")
	if lines[0] != strings.Repeat(" ", 8)+"ab" {
		t.Errorf("line 0 = %q, want 8-space gutter + ab", lines[0])
	}
	if lines[1] != strings.Repeat(" ", 8)+"cdef" {
		t.Errorf("line 1 = %q, want 8-space gutter + cdef", lines[1])
	}
}

func TestCenterBlockLeavesWideContentAlone(t *testing.T) {
	in := "ab\ncdef"
	if out := centerBlock(in, 4, 76); out != in {
		t.Errorf("block at terminal width must pass through, got %q", out)
	}
	wide := strings.Repeat("x", 120)
	if out := centerBlock(wide, 140, 76); out != wide {
		t.Errorf("block wider than maxW must pass through, got %q", out)
	}
}

func TestCenterBlockKeepsBlankLinesBlank(t *testing.T) {
	out := centerBlock("ab\n\ncdef", 20, 76)
	lines := strings.Split(out, "\n")
	if lines[1] != "" {
		t.Errorf("blank line = %q, want empty (no trailing spaces)", lines[1])
	}
}

// The results header shows the live input while focused (space from
// results): keystrokes after space must be visible, not typed blind
// into a box that still renders the stale submitted query.
func TestResultsHeaderShowsLiveInputWhenFocused(t *testing.T) {
	base := ResultsData{
		Query:   "one piece",
		Mode:    provider.ModeAnime,
		Results: []provider.SearchResult{{Title: "ONE PIECE", ID: "21"}},
		Width:   100,
		Height:  24,
		Accent:  ResolveAccent(model.KindAnime, "Auto"),
	}
	plain := RenderResultsScreen(base)
	if !strings.Contains(plain, "› one piece") {
		t.Errorf("unfocused header must show the submitted query, got:\n%s", plain)
	}
	focused := base
	focused.InputFocused = true
	focused.InputView = "› frieren▌"
	out := RenderResultsScreen(focused)
	if !strings.Contains(out, "› frieren") {
		t.Errorf("focused header must show the live input, got:\n%s", out)
	}
	if strings.Contains(out, "› one piece") {
		t.Errorf("focused header must not show the stale query, got:\n%s", out)
	}
}
