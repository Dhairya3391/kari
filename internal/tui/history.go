package tui

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"

	"kari/internal/history"
	"kari/internal/provider"
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
	titlePadded := titleTrunc + strings.Repeat(" ", max(0, titleW-lipgloss.Width(titleTrunc)))
	if isFocused {
		titlePadded = st.Current.Render(titleTrunc) + strings.Repeat(" ", max(0, titleW-lipgloss.Width(titleTrunc)))
	}

	kindStr := st.Dim.Render(fmt.Sprintf("%-7s", truncate(kind, 7)))
	posFormatted := fmt.Sprintf("%-14s", truncate(pos, 14))
	posStr := st.Dim.Render(posFormatted)
	if isFocused {
		posStr = st.Current.Render(posFormatted)
	}

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
	// 4. Episode title if known
	if strings.TrimSpace(entry.EpisodeTitle) != "" {
		parts = append(parts, st.Current.Render(truncate(strings.TrimSpace(entry.EpisodeTitle), width-2)))
	}

	// 5. Timestamp and completion details
	if entry.PositionSecs > 0 && entry.DurationSecs > 0 {
		pct := int(entry.PercentComplete * 100)
		if pct == 0 && entry.DurationSecs > 0 {
			pct = int((entry.PositionSecs / entry.DurationSecs) * 100)
		}
		timeStr := fmt.Sprintf("%s / %s (%d%%)", formatTimeMMSS(entry.PositionSecs), formatTimeMMSS(entry.DurationSecs), pct)
		parts = append(parts, st.Dim.Render(timeStr))
	} else if entry.Complete {
		parts = append(parts, st.Ok.Render("completed"))
	}

	if g.WatchedCount > 1 {
		parts = append(parts, st.Dim.Render(fmt.Sprintf("%d watched", g.WatchedCount)))
	}

	// 6. Relative time line (e.g. "9 minutes ago")
	relTime := formatLongRelativeTime(entry.WatchedAt)
	parts = append(parts, st.Dim.Render(relTime))

	// 7. Context hint for anime
	if entry.Mode == "anime" || entry.Mode == string(provider.ModeAnime) {
		parts = append(parts, "", st.Dim.Render("t  status / rewatch"))
	}

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

// sortGroupsByDateBucket reorders groups in Today → This week → Earlier
// order, matching renderHistoryList exactly. Within each bucket the
// original (newest-first) order is preserved. finishedTab selects
// FarthestComplete instead of ContinueEntry for the timestamp, mirroring
// the renderer's tab-aware entry selection.
func sortGroupsByDateBucket(groups []history.Group, finishedTab bool) []history.Group {
	now := time.Now()
	bucket := func(g history.Group) int {
		entry := g.ContinueEntry
		if finishedTab {
			entry = g.FarthestComplete
		}
		d := now.Sub(entry.WatchedAt)
		if d < 24*time.Hour && now.Day() == entry.WatchedAt.Day() {
			return 0 // Today
		}
		if d < 7*24*time.Hour {
			return 1 // This week
		}
		return 2 // Earlier
	}
	out := make([]history.Group, len(groups))
	copy(out, groups)
	sort.SliceStable(out, func(i, j int) bool {
		return bucket(out[i]) < bucket(out[j])
	})
	return out
}
