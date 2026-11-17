package tui

// Package screens contains pure render functions for each UI screen.
// This file handles the Live TV / Sports guide view (plan §5.5).
//
// Live is a two-pane guide: a grouped, scannable list on the left and an
// "On now" inspector on the right. Temporal grouping and category headings
// come from service.BuildLiveGuide; this file only renders the prepared rows.
// The artwork slot is sized here to the same dimensions the fetch uses, so a
// persistent terminal image can never paint over the list beneath it.

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"

	"kari/internal/provider"
	"kari/internal/service"
)

const (
	// liveInspectorMinWidth is the content width at which the guide can show
	// the inspector without squeezing the channel list below a readable size.
	liveInspectorMinWidth = 70
	liveInspectorMaxWidth = 36
	// liveInspectorGap is " │ " between the list and the inspector.
	liveInspectorGap = 3
	// liveHintRows reserves room for the scroll-up and scroll-down hints.
	liveHintRows = 2
	// livePosterInset keeps the artwork a cell off the inspector's left edge.
	livePosterInset = 1
	// liveMinInspectorHeight leaves room for the fixed artwork slot plus the
	// text beneath it; below it the guide falls back to a single column.
	liveMinInspectorHeight = 17
	// liveStatusCellWidth is the fixed width of the LIVE / time label column.
	liveStatusCellWidth = 6
	// liveProviderWidth is the fixed width of the integration alias column.
	liveProviderWidth = 10
)

// RenderLiveResultsScreen renders the Live TV / Sports guide view.
func RenderLiveResultsScreen(data ResultsData) string {
	st := NewStyles(data.Accent)
	now := time.Now()
	width := max(20, data.Width)

	// Rebuild the guide when it is missing or stale. A guide built from a
	// different result slice would put the highlight on the wrong row.
	if data.LiveGuide == nil || !liveGuideMatches(data) {
		guide := service.BuildLiveGuide(data.Results, data.SelectedIndex, now)
		data.LiveGuide = &guide
	}
	showProvider := false
	for _, result := range data.Results {
		if result.Provider != "" {
			showProvider = true
			break
		}
	}

	showInspector := width >= liveInspectorMinWidth && data.Height >= liveMinInspectorHeight
	inspectorW := 0
	listW := width
	if showInspector {
		inspectorW = min(liveInspectorMaxWidth, max(24, width*30/100))
		listW = width - inspectorW - liveInspectorGap
		if listW < 40 {
			listW = 40
			inspectorW = max(20, width-listW-liveInspectorGap)
		}
	}

	strip := renderLiveStatusStrip(data, st, width, showInspector, now)
	if len(data.Results) == 0 {
		return strings.Join(append(strip,
			"",
			"No live channels available.",
			st.Dim.Render(liveEmptyHint(data)),
		), "\n")
	}

	viewport := max(3, data.Height-len(strip)-1-liveHintRows)
	guideRows, selectedRow := liveGuideRows(data, now, listW, showProvider, st)
	visible, above, below := windowRows(guideRows, selectedRow, viewport)

	var list []string
	if hint := windowHint(above, true, st); hint != "" {
		list = append(list, hint)
	}
	list = append(list, visible...)
	if hint := windowHint(below, false, st); hint != "" {
		list = append(list, hint)
	}
	list = padLines(list, viewport+liveHintRows)

	body := make([]string, 0, len(strip)+1+len(list))
	body = append(body, strip...)
	body = append(body, "")

	if !showInspector {
		for _, line := range list {
			body = append(body, padCells(line, width))
		}
		return strings.Join(body, "\n")
	}

	inspector := renderLiveInspector(data, now, inspectorW, len(list), st)
	for i, line := range list {
		body = append(body, padCells(line, listW)+" │ "+padCells(inspector[i], inspectorW))
	}
	return strings.Join(body, "\n")
}

// renderLiveStatusStrip builds the count strip that replaces the generic
// results count. The section containing the selection is accented.
func renderLiveStatusStrip(data ResultsData, st Styles, width int, showInspector bool, now time.Time) []string {
	counts := service.LiveCounts{}
	active := liveActiveSection(data)
	if data.LiveGuide != nil {
		counts = data.LiveGuide.Counts
	}

	tokens := []struct {
		label string
		count int
		kind  service.LiveSectionKind
	}{
		{"LIVE NOW", counts.Now, service.LiveSectionNow},
		{"UPCOMING", counts.Upcoming, service.LiveSectionUpcoming},
		{"CHANNELS", counts.Channels, service.LiveSectionChannels},
	}
	parts := make([]string, 0, len(tokens))
	for _, token := range tokens {
		label := fmt.Sprintf("%s %d", token.label, token.count)
		if token.kind == active {
			parts = append(parts, st.Current.Render(label))
		} else {
			parts = append(parts, st.Dim.Render(label))
		}
	}
	left := strings.Join(parts, "   ")

	right := "live feed"
	if !data.LiveUpdatedAt.IsZero() {
		right = "updated " + data.LiveUpdatedAt.Format("15:04:05")
	}
	if data.LiveSources > 0 {
		right = fmt.Sprintf("%d sources · %s", data.LiveSources, right)
	}
	right = st.Dim.Render(right)

	var first string
	if lipgloss.Width(left)+lipgloss.Width(right)+2 <= width {
		first = left + strings.Repeat(" ", width-lipgloss.Width(left)-lipgloss.Width(right)) + right
	} else {
		first = padCells(left, max(10, width-lipgloss.Width(right)-1)) + " " + right
	}

	strip := []string{first, st.Dim.Render(strings.Repeat("─", width))}
	if !showInspector {
		if label, title := liveSelectedSummary(data, now); label != "" {
			strip = append(strip, st.Current.Render(label)+" "+st.Dim.Render(truncate(title, width-lipgloss.Width(label)-1)))
		}
	}
	return strip
}

// liveGuideRows renders the prepared guide rows in display order and returns
// the rendered position of the selected row. The returned index is in the same
// space as the rendered rows (which include section headings and spacers), so
// it can be passed directly to windowRows; LiveGuide.SelectedRow indexes the
// unprepared guide.Rows and is not usable for scrolling.
func liveGuideRows(data ResultsData, now time.Time, width int, showProvider bool, st Styles) ([]string, int) {
	if data.LiveGuide == nil {
		return nil, -1
	}
	var rows []string
	selectedRow := -1
	for _, section := range data.LiveGuide.Sections {
		rows = append(rows, st.Bold.Render(fmt.Sprintf("%s · %d", strings.ToUpper(section.Title), section.Count)))
		for _, row := range data.LiveGuide.Rows {
			if row.Section != section.Kind {
				continue
			}
			if row.Kind == service.LiveRowCategory {
				rows = append(rows, st.Dim.Render(fmt.Sprintf("  %s · %d", row.Label, row.Count)))
				continue
			}
			if row.Index < 0 || row.Index >= len(data.Results) {
				continue
			}
			name := liveProviderName(data, data.Results[row.Index])
			if row.Index == data.SelectedIndex {
				selectedRow = len(rows)
			}
			rows = append(rows, renderLiveRow(
				data.Results[row.Index],
				row.Index == data.SelectedIndex,
				name,
				showProvider,
				now,
				width,
				st,
			))
		}
		rows = append(rows, "")
	}
	return rows, selectedRow
}

// renderLiveRow renders one selectable stream: cursor, status, title, and the
// integration alias when any result carries one.
func renderLiveRow(result provider.SearchResult, focused bool, providerName string, showProvider bool, now time.Time, width int, st Styles) string {
	cursor := "  "
	if focused {
		cursor = st.Cursor.Render("▌ ")
	}

	label := service.LiveTimeLabel(result, now)
	if label == "LIVE" {
		label = "● LIVE"
	}
	cell := fmt.Sprintf("%-*s", liveStatusCellWidth, label)
	if focused || label == "● LIVE" {
		cell = st.Current.Render(cell)
	} else {
		cell = st.Dim.Render(cell)
	}

	// cursor (2) + status (6) + two 2-cell gaps, plus the provider column
	// when present. Every row must fit listW exactly or padCells clips it.
	fixed := 2 + liveStatusCellWidth + 2 + 2
	if showProvider {
		fixed += liveProviderWidth
	}
	titleW := max(12, width-fixed)
	title := truncate(result.Title, titleW)
	if focused {
		title = st.Current.Render(title)
	} else if title == "" {
		title = st.Dim.Render("—")
	}
	titleCell := title + strings.Repeat(" ", max(0, titleW-lipgloss.Width(title)))

	row := cursor + cell + "  " + titleCell
	if showProvider {
		if providerName == "" {
			providerName = "—"
		}
		row += "  " + st.Dim.Render(fmt.Sprintf("%-*s", liveProviderWidth, truncate(providerName, liveProviderWidth)))
	}
	return row
}

// renderLiveInspector builds the fixed-height "On now" pane beside the list.
func renderLiveInspector(data ResultsData, now time.Time, width, height int, st Styles) []string {
	lines := []string{st.Dim.Render("ON NOW"), ""}
	poster := strings.Split(fixedBlock(data.PosterBlock, livePosterMaxCols, livePosterRows), "\n")
	for _, line := range poster {
		lines = append(lines, strings.Repeat(" ", livePosterInset)+line)
	}
	lines = append(lines, "")

	if data.SelectedIndex < 0 || data.SelectedIndex >= len(data.Results) {
		lines = append(lines, st.Dim.Render("Select a channel"))
		return padLines(lines, height)
	}
	selected := data.Results[data.SelectedIndex]
	lines = append(lines, strings.Split(wrapFixed(selected.Title, width, 2), "\n")...)

	meta := service.LiveCategory(selected)
	if name := liveProviderName(data, selected); name != "" {
		meta += " · " + name
	}
	lines = append(lines, st.Dim.Render(truncate(meta, width)))

	if service.LiveTimeLabel(selected, now) == "LIVE" {
		elapsed := service.LiveElapsedLabel(selected, now)
		if elapsed == "" {
			if selected.Group == "Channel" || strings.Contains(service.LiveCategory(selected), "24/7") {
				elapsed = "24/7"
			} else {
				elapsed = "now"
			}
		}
		lines = append(lines, st.Current.Render("● ON AIR")+" "+st.Dim.Render(elapsed))
	} else if !selected.StartsAt.IsZero() {
		lines = append(lines, st.Dim.Render("STARTS "+selected.StartsAt.Format("15:04")))
	} else {
		lines = append(lines, st.Dim.Render("—"))
	}

	lines = append(lines, "", st.Dim.Render("UP NEXT"))
	nextTime, nextTitle := service.LiveUpNext(data.Results, data.SelectedIndex, now)
	if nextTime == "" {
		lines = append(lines, st.Dim.Render("  —"))
	} else {
		stamp := st.Current.Render("  " + nextTime)
		lines = append(lines, stamp+" "+st.Dim.Render(truncate(nextTitle, width-lipgloss.Width(stamp)-1)))
	}
	lines = append(lines, "", st.Dim.Render("Enter to watch"))
	return padLines(lines, height)
}

// liveSelectedSummary provides the compact on-now line used when the inspector
// is hidden on narrow terminals.
func liveSelectedSummary(data ResultsData, now time.Time) (string, string) {
	if data.SelectedIndex < 0 || data.SelectedIndex >= len(data.Results) {
		return "", ""
	}
	selected := data.Results[data.SelectedIndex]
	return service.LiveTimeLabel(selected, now), selected.Title
}

func liveActiveSection(data ResultsData) service.LiveSectionKind {
	if data.LiveGuide == nil {
		return service.LiveSectionKind(-1)
	}
	for _, row := range data.LiveGuide.Rows {
		if row.Kind == service.LiveRowResult && row.Index == data.SelectedIndex {
			return row.Section
		}
	}
	return service.LiveSectionKind(-1)
}

func liveProviderName(data ResultsData, result provider.SearchResult) string {
	if result.Provider == "" {
		return ""
	}
	if name := data.ProviderNames[result.Provider]; name != "" {
		return name
	}
	return result.Provider
}

// liveGuideMatches reports whether the prepared guide's selected row still
// points at the currently selected result.
func liveGuideMatches(data ResultsData) bool {
	if data.LiveGuide == nil {
		return false
	}
	row := data.LiveGuide.SelectedRow
	if row < 0 || row >= len(data.LiveGuide.Rows) {
		return false
	}
	entry := data.LiveGuide.Rows[row]
	return entry.Kind == service.LiveRowResult && entry.Index == data.SelectedIndex
}

func liveEmptyHint(data ResultsData) string {
	if data.LiveSources > 0 {
		return "r refresh · Space new search · Tab switch mode"
	}
	return "Space new search · Tab switch mode"
}
