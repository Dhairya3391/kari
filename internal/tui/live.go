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
	result       *provider.SearchResult
	group        liveGroup
	timeLabel    string // "live" or "HH:MM"
	providerName string // integration serving the stream, e.g. "Famelack"
}

type liveChannelGroup struct {
	name    string
	indices []int
}

// classifyLiveResult maps a SearchResult into the appropriate liveGroup
// using only the explicit scheduling fields: Live marks live-now, a future
// StartsAt sorts into Today/Tomorrow/Later with the local time as label,
// and a Channel group overrides the schedule column. A result with no
// scheduling data at all lands in Later with a placeholder label.
func classifyLiveResult(r provider.SearchResult) (liveGroup, string) {
	return classifyLiveResultAt(r, time.Now())
}

func classifyLiveResultAt(r provider.SearchResult, now time.Time) (liveGroup, string) {

	if r.Group == "Channel" {
		return liveGroupChannels, "—"
	}
	if !r.StartsAt.IsZero() {
		diff := r.StartsAt.Sub(now)
		if diff <= 0 {
			if now.Sub(r.StartsAt) > provider.LiveEventMaxDuration {
				return liveGroupLater, "Ended"
			}
			return liveGroupNow, "live"
		}
		if sameLiveDate(r.StartsAt, now) {
			return liveGroupToday, r.StartsAt.Format("15:04")
		}
		tomorrow := now.AddDate(0, 0, 1)
		if sameLiveDate(r.StartsAt, tomorrow) {
			return liveGroupTomorrow, r.StartsAt.Format("15:04")
		}
		return liveGroupLater, r.StartsAt.Format("Mon 15:04")
	}
	if r.Live {
		return liveGroupNow, "live"
	}
	return liveGroupLater, "—"
}

func sameLiveDate(left, right time.Time) bool {
	leftYear, leftMonth, leftDay := left.Date()
	rightYear, rightMonth, rightDay := right.Date()
	return leftYear == rightYear && leftMonth == rightMonth && leftDay == rightDay
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

	// Keep only result indices in group buckets. Copying every SearchResult
	// here on every cursor move creates large amounts of garbage for catalogs
	// with thousands of channels.
	var byGroup [liveGroupChannels + 1][]int
	showProvider := false
	now := time.Now()
	selectedGroup, selectedRank := liveGroup(-1), -1
	for i := range data.Results {
		result := &data.Results[i]
		group, _ := classifyLiveResultAt(*result, now)
		if result.Provider != "" {
			showProvider = true
		}
		if i == data.SelectedIndex {
			selectedGroup = group
			selectedRank = len(byGroup[group])
		}
		byGroup[group] = append(byGroup[group], i)
	}
	channelGroups := make([]liveChannelGroup, 0)
	channelGroupIndex := make(map[string]int)
	selectedChannelBucket, selectedChannelOffset := -1, -1
	for _, index := range byGroup[liveGroupChannels] {
		result := &data.Results[index]
		name := categoryFromGenres(result.Genres)
		if name == "" {
			name = "Other channels"
		}
		bucket, ok := channelGroupIndex[name]
		if !ok {
			bucket = len(channelGroups)
			channelGroupIndex[name] = bucket
			channelGroups = append(channelGroups, liveChannelGroup{name: name})
		}
		if index == data.SelectedIndex {
			selectedChannelBucket = bucket
			selectedChannelOffset = len(channelGroups[bucket].indices)
		}
		channelGroups[bucket].indices = append(channelGroups[bucket].indices, index)
	}

	totalRows := 0
	selectedPosition := -1
	for _, group := range groupOrder {
		count := len(byGroup[group])
		if count == 0 {
			continue
		}
		if group == selectedGroup {
			if group != liveGroupChannels {
				selectedPosition = totalRows + 1 + selectedRank
			}
		}
		if group == liveGroupChannels {
			totalRows += count + len(channelGroups) + 2
			if selectedChannelBucket >= 0 {
				selectedPosition = totalRows - count - len(channelGroups) - 2 + 1 + selectedChannelBucket + selectedChannelOffset
				for i := 0; i < selectedChannelBucket; i++ {
					selectedPosition += len(channelGroups[i].indices)
				}
			}
		} else {
			totalRows += count + 2 // section heading and the section spacer
		}
	}
	start, end, above, below := windowRange(totalRows, selectedPosition, data.Height, 3)
	if hint := windowHint(above, true, st); hint != "" {
		rows = append(rows, hint)
	}
	flatPosition := 0
	for _, group := range groupOrder {
		indices := byGroup[group]
		if len(indices) == 0 {
			continue
		}
		if flatPosition >= start && flatPosition < end {
			heading := liveGroupName[group]
			if group == liveGroupChannels {
				heading = fmt.Sprintf("Channels · %d", len(indices))
			} else {
				heading = fmt.Sprintf("%s · %d", heading, len(indices))
			}
			rows = append(rows, st.Dim.Render(heading))
		}
		flatPosition++
		if group == liveGroupChannels {
			for _, channelGroup := range channelGroups {
				if flatPosition >= start && flatPosition < end {
					rows = append(rows, st.Dim.Render(fmt.Sprintf("  %s · %d", channelGroup.name, len(channelGroup.indices))))
				}
				flatPosition++
				for _, index := range channelGroup.indices {
					if flatPosition >= start && flatPosition < end {
						result := &data.Results[index]
						_, timeLabel := classifyLiveResultAt(*result, now)
						providerName := data.ProviderNames[result.Provider]
						if providerName == "" {
							providerName = result.Provider
						}
						entry := liveEntry{result: result, group: group, timeLabel: timeLabel, providerName: providerName}
						rows = append(rows, renderLiveRow(entry, index == data.SelectedIndex, showProvider, data.Width, st))
					}
					flatPosition++
				}
			}
			if flatPosition >= start && flatPosition < end {
				rows = append(rows, "")
			}
			flatPosition++
			continue
		}
		for _, index := range indices {
			if flatPosition >= start && flatPosition < end {
				result := &data.Results[index]
				_, timeLabel := classifyLiveResultAt(*result, now)
				providerName := data.ProviderNames[result.Provider]
				if providerName == "" {
					providerName = result.Provider
				}
				entry := liveEntry{
					result:       result,
					group:        group,
					timeLabel:    timeLabel,
					providerName: providerName,
				}
				rows = append(rows, renderLiveRow(entry, index == data.SelectedIndex, showProvider, data.Width, st))
			}
			flatPosition++
		}
		if flatPosition >= start && flatPosition < end {
			rows = append(rows, "")
		}
		flatPosition++
	}
	if hint := windowHint(below, false, st); hint != "" {
		rows = append(rows, hint)
	}

	return strings.Join(rows, "\n")
}

// renderLiveRow renders a single live result row with cursor, live indicator,
// title, group/category, provider, and time label. The provider column is
// omitted when showProvider is false so fixtures without provider attribution
// keep the compact layout.
func renderLiveRow(e liveEntry, isFocused, showProvider bool, width int, st Styles) string {
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
	if category == "" || category == "Channel" {
		category = categoryFromGenres(e.result.Genres)
	}
	catW := 14
	catStr := st.Dim.Render(truncate(category, catW) + strings.Repeat(" ", max(0, catW-lipgloss.Width(category))))

	// Title width = available space minus fixed cols.
	provW := 10
	fixedW := lipgloss.Width(cursor) + lipgloss.Width(liveIcon) + catW + len(timeCol) + 4
	if showProvider {
		fixedW += provW + 2
	}
	titleW := max(20, width-fixedW)

	title := e.result.Title
	titleTrunc := truncate(title, titleW)
	titlePadded := titleTrunc + strings.Repeat(" ", max(0, titleW-lipgloss.Width(titleTrunc)))
	if isFocused {
		titlePadded = st.Current.Render(titleTrunc) + strings.Repeat(" ", max(0, titleW-lipgloss.Width(titleTrunc)))
	}

	timeStr := st.Dim.Render(timeCol)
	if isFocused {
		timeStr = st.Current.Render(timeCol)
	}

	row := fmt.Sprintf("%s%s%s  %s", cursor, liveIcon, titlePadded, catStr)
	if showProvider {
		provider := e.providerName
		if provider == "" {
			provider = "—"
		}
		name := truncate(provider, provW)
		providerStr := st.Dim.Render(name + strings.Repeat(" ", max(0, provW-lipgloss.Width(name))))
		row += "  " + providerStr
	}
	return row + "  " + timeStr
}

// categoryFromGenres picks the first genre as a display category, or empty.
func categoryFromGenres(genres []string) string {
	if len(genres) == 0 {
		return ""
	}
	return genres[0]
}
