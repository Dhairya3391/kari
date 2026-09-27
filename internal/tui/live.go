package tui

// Package screens contains pure render functions for each UI screen.
// This file handles the Live TV / Sports results view (plan §5.5).
//
// Live results are grouped into: "Live now", "Today", "Tomorrow", "Later",
// and optionally "Channels". The grouping comes exclusively from the
// explicit SearchResult.Live, SearchResult.StartsAt, and SearchResult.Group
// fields — the Year field is never interpreted as schedule text.

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"

	"kari/internal/provider"
)

// liveGroup names the display sections for live results.
type liveGroup int

const (
	liveGroupNow      liveGroup = iota // ● live right now
	liveGroupToday                     // scheduled for later today
	liveGroupTomorrow                  // scheduled for tomorrow
	liveGroupLater                     // more than a day away
	liveGroupChannels                  // static channel entries
)

// liveGroupName is the dim header shown above each group.
var liveGroupName = map[liveGroup]string{
	liveGroupNow:      "Live now",
	liveGroupToday:    "Today",
	liveGroupTomorrow: "Tomorrow",
	liveGroupLater:    "Later",
	liveGroupChannels: "Channels",
}

// liveEntry pairs a result with its resolved group and display time.
type liveEntry struct {
	result    provider.SearchResult
	origIndex int
	group     liveGroup
	timeLabel string // "live" or "HH:MM"
}

// classifyLiveResult maps a SearchResult into the appropriate liveGroup
// using only the explicit scheduling fields: Live marks live-now, a future
// StartsAt sorts into Today/Tomorrow/Later with the local time as label,
// and a Channel group overrides the schedule column. A result with no
// scheduling data at all lands in Later with a placeholder label.
func classifyLiveResult(r provider.SearchResult) (liveGroup, string) {
	now := time.Now()

	if r.Group == "Channel" {
		return liveGroupChannels, "—"
	}
	if r.Live {
		return liveGroupNow, "live"
	}
	if !r.StartsAt.IsZero() {
		diff := r.StartsAt.Sub(now)
		if diff <= 0 {
			return liveGroupNow, "live"
		}
		if r.StartsAt.Day() == now.Day() && r.StartsAt.Month() == now.Month() {
			return liveGroupToday, r.StartsAt.Format("15:04")
		}
		tomorrow := now.AddDate(0, 0, 1)
		if r.StartsAt.Day() == tomorrow.Day() && r.StartsAt.Month() == tomorrow.Month() {
			return liveGroupTomorrow, r.StartsAt.Format("15:04")
		}
		return liveGroupLater, r.StartsAt.Format("Mon 15:04")
	}
	return liveGroupLater, "—"
}

// groupOrder is the section display order per plan §5.5.
var groupOrder = []liveGroup{
	liveGroupNow,
	liveGroupToday,
	liveGroupTomorrow,
	liveGroupLater,
	liveGroupChannels,
}

// RenderLiveResultsScreen renders the Live TV / Sports results view.
// Results are grouped into temporal sections with a time column.
func RenderLiveResultsScreen(data ResultsData) string {
	st := NewStyles(data.Accent)

	// Header
	var rows []string
	rows = append(rows, queryLine(data))
	rows = append(rows, "")

	if len(data.Results) == 0 {
		rows = append(rows,
			"No live content available.",
			st.Dim.Render("Press r to refresh."),
		)
		return strings.Join(rows, "\n")
	}

	// Classify each result.
	byGroup := make(map[liveGroup][]liveEntry)
	for i, r := range data.Results {
		g, label := classifyLiveResult(r)
		byGroup[g] = append(byGroup[g], liveEntry{
			result:    r,
			origIndex: i,
			group:     g,
			timeLabel: label,
		})
	}

	// Render each group in order, flattened so the list can scroll.
	// SelectedIndex addresses entries only; selPos tracks the same entry
	// in the flat row list (headers included).
	var flat []string
	selPos := -1
	entryIdx := 0
	for _, g := range groupOrder {
		entries, ok := byGroup[g]
		if !ok {
			continue
		}
		// Section header
		flat = append(flat, st.Dim.Render(liveGroupName[g]))

		for _, e := range entries {
			if entryIdx == data.SelectedIndex {
				selPos = len(flat)
			}
			flat = append(flat, renderLiveRow(e, entryIdx == data.SelectedIndex, data.Width, st))
			entryIdx++
		}
		flat = append(flat, "") // blank line between groups
	}

	// 2 header lines above the list ("› query", "").
	rows = append(rows, applyWindow(flat, selPos, data.Height, 3, st)...)

	return strings.Join(rows, "\n")
}

// renderLiveRow renders a single live result row with cursor, live indicator,
// title, group/category, and time label.
func renderLiveRow(e liveEntry, isFocused bool, width int, st Styles) string {
	cursor := "  "
	if isFocused {
		cursor = st.Cursor.Render("▌ ")
	}

	// Live indicator: ● for live now, space otherwise.
	liveIcon := "  "
	if e.group == liveGroupNow {
		liveIcon = st.Ok.Render("● ")
	}

	// Time label (right-aligned in a fixed column).
	timeCol := fmt.Sprintf("%6s", e.timeLabel)

	// Category / group label (dim).
	category := e.result.Group
	if category == "" {
		category = categoryFromGenres(e.result.Genres)
	}
	catW := 14
	catStr := st.Dim.Render(truncate(category, catW) + strings.Repeat(" ", max(0, catW-lipgloss.Width(category))))

	// Title width = available space minus fixed cols.
	fixedW := lipgloss.Width(cursor) + lipgloss.Width(liveIcon) + catW + len(timeCol) + 4
	titleW := max(20, width-fixedW)

	title := e.result.Title
	titleTrunc := truncate(title, titleW)
	titlePadded := titleTrunc + strings.Repeat(" ", max(0, titleW-lipgloss.Width(titleTrunc)))
	if isFocused {
		titlePadded = st.Bold.Render(titleTrunc) + strings.Repeat(" ", max(0, titleW-lipgloss.Width(titleTrunc)))
	}

	return fmt.Sprintf("%s%s%s  %s  %s", cursor, liveIcon, titlePadded, catStr, st.Dim.Render(timeCol))
}

// categoryFromGenres picks the first genre as a display category, or empty.
func categoryFromGenres(genres []string) string {
	if len(genres) == 0 {
		return ""
	}
	return genres[0]
}
