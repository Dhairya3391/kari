package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"

	"kari/internal/history"
)

// HistoryTab identifies the active section tab.
type HistoryTab int

const (
	HistoryTabContinue HistoryTab = iota
	HistoryTabFinished
)

// HistoryData carries state needed to render the History screen.
type HistoryData struct {
	ActiveTab        HistoryTab
	ContinueCount    int
	FinishedCount    int
	Groups           []history.Group
	SelectedIndex    int
	PosterBlock      string
	SelectedGroup    *history.Group
	ConfirmClearAll  bool
	ConfirmDeleteOne bool
	FilterQuery      string
	Width            int
	Height           int
	Accent           lipgloss.AdaptiveColor
}

// RenderHistoryScreen renders the History screen per KARI_TUI_SPEC §4.6.
func RenderHistoryScreen(data HistoryData) string {
	st := NewStyles(data.Accent)

	// Confirm inline dialogs
	if data.ConfirmClearAll {
		return renderInlineConfirm("clear all history? y / n", data.Width, st)
	}
	if data.ConfirmDeleteOne {
		return renderInlineConfirm("delete this title from history? y / n", data.Width, st)
	}

	var rows []string

	// 1. Tab bar: CONTINUE 15     finished 6
	tabLabels := []string{
		fmt.Sprintf("CONTINUE %d", data.ContinueCount),
		fmt.Sprintf("finished %d", data.FinishedCount),
	}
	activeTabIdx := 0
	if data.ActiveTab == HistoryTabFinished {
		activeTabIdx = 1
	}
	tabStrip := RenderTabs(tabLabels, activeTabIdx, data.Accent)
	rows = append(rows, tabStrip, "")

	if len(data.Groups) == 0 {
		emptyMsg := []string{
			"",
			"Nothing here yet. Play something and it shows up.",
		}
		return strings.Join(append(rows, emptyMsg...), "\n")
	}

	// The inspector pane needs its poster: without artwork it only
	// repeats the selected row's title/kind/position as floating text,
	// which reads as overlap. Text-only falls back to one clean column.
	showPosterPane := data.Width >= 100 && strings.TrimSpace(data.PosterBlock) != ""

	if !showPosterPane {
		listCol := renderHistoryList(data, data.Width, st)
		return singleColumn(strings.Join(append(rows, listCol), "\n"), data.Width, 110)
	}

	// 2-column layout: Left is time-grouped list, Right is detail card + poster
	leftW := data.Width * 62 / 100
	if leftW < 45 {
		leftW = 45
	}
	rightW := data.Width - leftW - 2

	leftCol := renderHistoryList(data, leftW, st)
	rightCol := renderHistoryDetails(data, rightW, st)

	joined := lipgloss.JoinHorizontal(lipgloss.Top,
		lipgloss.NewStyle().Width(leftW).Render(leftCol),
		"  ",
		lipgloss.NewStyle().Width(rightW).Render(rightCol),
	)

	return strings.Join(append(rows, joined), "\n")
}

func renderHistoryList(data HistoryData, width int, st Styles) string {
	var rows []string
	selPos := -1

	// Group into Today, This week, Earlier
	now := time.Now()
	todayGroups := []int{}
	weekGroups := []int{}
	earlierGroups := []int{}

	for i, g := range data.Groups {
		entry := g.ContinueEntry
		if data.ActiveTab == HistoryTabFinished {
			entry = g.FarthestComplete
		}
		d := now.Sub(entry.WatchedAt)
		if d < 24*time.Hour && now.Day() == entry.WatchedAt.Day() {
			todayGroups = append(todayGroups, i)
		} else if d < 7*24*time.Hour {
			weekGroups = append(weekGroups, i)
		} else {
			earlierGroups = append(earlierGroups, i)
		}
	}

	renderBucket := func(title string, indices []int) {
		if len(indices) == 0 {
			return
		}
		if len(rows) > 0 {
			rows = append(rows, "")
		}
		rows = append(rows, st.Dim.Render(title))

		for _, idx := range indices {
			g := data.Groups[idx]
			isFocused := idx == data.SelectedIndex
			if isFocused {
				selPos = len(rows)
			}
			row := renderHistoryRow(g, idx, isFocused, data, width, st)
			rows = append(rows, row)
		}
	}

	renderBucket("Today", todayGroups)
	renderBucket("This week", weekGroups)
	renderBucket("Earlier", earlierGroups)

	// Tab strip + blank line above the list.
	return strings.Join(applyWindow(rows, selPos, data.Height, 3, st), "\n")
}

func renderHistoryRow(g history.Group, index int, isFocused bool, data HistoryData, width int, st Styles) string {
	entry := g.ContinueEntry
	if data.ActiveTab == HistoryTabFinished {
		entry = g.FarthestComplete
	}

	cursor := "  "
	if isFocused {
		cursor = st.Cursor.Render("▌ ")
	}

	title := g.Title
	if title == "" {
		title = entry.Title
	}

	kind := strings.ToLower(entry.MediaType)
	if entry.Mode != "" {
		kind = strings.ToLower(string(entry.Mode))
	}
	if kind == "" {
		kind = "title"
	}

	pos := formatContinuePosition(entry)

	ratio := 0.0
	if entry.DurationSecs > 0 {
		ratio = entry.PositionSecs / entry.DurationSecs
	}
	if entry.Complete {
		ratio = 1.0
	}
	bar := RenderProgressBar(ratio, 10, data.Accent)

	// Width calculations
	kindW := 7
	posW := 14
	barW := 10
	fixedW := 2 + kindW + posW + barW + 8
	titleW := width - fixedW
	if titleW < 16 {
		titleW = 16
	}

	titleTrunc := truncate(title, titleW)
	titlePadded := titleTrunc + strings.Repeat(" ", max(1, titleW-lipgloss.Width(titleTrunc)))
	if isFocused {
		titlePadded = st.Bold.Render(titleTrunc) + strings.Repeat(" ", max(1, titleW-lipgloss.Width(titleTrunc)))
	}

	kindStr := st.Dim.Render(fmt.Sprintf("%-7s", truncate(kind, 7)))
	posStr := st.Dim.Render(fmt.Sprintf("%-14s", truncate(pos, 14)))

	return fmt.Sprintf("%s%s  %s  %s  %s", cursor, titlePadded, kindStr, posStr, bar)
}

func renderHistoryDetails(data HistoryData, width int, st Styles) string {
	if data.SelectedIndex < 0 || data.SelectedIndex >= len(data.Groups) {
		return ""
	}
	g := data.Groups[data.SelectedIndex]
	entry := g.ContinueEntry
	if data.ActiveTab == HistoryTabFinished {
		entry = g.FarthestComplete
	}

	var parts []string

	// 1. Poster block
	if data.PosterBlock != "" {
		parts = append(parts, data.PosterBlock, "")
	}

	// 2. Title
	title := g.Title
	if title == "" {
		title = entry.Title
	}
	parts = append(parts, st.Bold.Render(title))

	// 3. Kind · position line
	kind := strings.ToLower(entry.MediaType)
	if entry.Mode != "" {
		kind = strings.ToLower(string(entry.Mode))
	}
	pos := formatContinuePosition(entry)
	meta := fmt.Sprintf("%s · %s", kind, pos)
	parts = append(parts, st.Dim.Render(meta))

	// 4. Relative time line (e.g. "9 minutes ago")
	relTime := formatLongRelativeTime(entry.WatchedAt)
	parts = append(parts, st.Dim.Render(relTime))

	return strings.Join(parts, "\n")
}

func formatLongRelativeTime(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		mins := int(d.Minutes())
		if mins == 1 {
			return "1 minute ago"
		}
		return fmt.Sprintf("%d minutes ago", mins)
	case d < 24*time.Hour:
		hrs := int(d.Hours())
		if hrs == 1 {
			return "1 hour ago"
		}
		return fmt.Sprintf("%d hours ago", hrs)
	case d < 7*24*time.Hour:
		days := int(d.Hours() / 24)
		if days == 1 {
			return "1 day ago"
		}
		return fmt.Sprintf("%d days ago", days)
	default:
		return t.Format("2006-01-02")
	}
}

func renderInlineConfirm(prompt string, width int, st Styles) string {
	return "\n\n" + st.Dim.Render("  "+prompt)
}
