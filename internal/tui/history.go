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
	ModeFilter       provider.ContentType
	ModeFilters      []HistoryModeFilter
	FilterQuery      string
	Filtering        bool
	Groups           []history.Group
	SelectedIndex    int
	PosterBlock      string
	ConfirmClearAll  bool
	ConfirmDeleteOne bool
	Width            int
	Height           int
	Accent           lipgloss.AdaptiveColor
}

// HistoryModeFilter describes a mode option and its item count in the
// active Continue or Finished section. The zero mode represents All.
type HistoryModeFilter struct {
	Mode  provider.ContentType
	Label string
	Count int
}

// RenderHistoryScreen renders the History screen per KARI_TUI_SPEC §4.6.
func RenderHistoryScreen(data HistoryData) string {
	st := NewStyles(data.Accent)
	layoutData := data
	layoutData.Width = min(data.Width, 130)

	// Confirm inline dialogs
	if data.ConfirmClearAll {
		return renderInlineConfirm("clear all history? y / n", data.Width, st)
	}
	if data.ConfirmDeleteOne {
		return renderInlineConfirm("delete this title from history? y / n", data.Width, st)
	}

	rows := []string{renderHistoryNavigation(layoutData)}
	rows = append(rows, "")
	listHeaderHeight := lipgloss.Height(strings.Join(rows, "\n"))
	if data.Filtering || strings.TrimSpace(data.FilterQuery) != "" {
		rows = append(rows, renderHistoryFilterLine(data, layoutData.Width, st), "")
		listHeaderHeight += 2
	}
	layoutData.Height = max(3, data.Height-listHeaderHeight)

	if len(data.Groups) == 0 {
		if query := strings.TrimSpace(data.FilterQuery); query != "" {
			mode := "history"
			if data.ModeFilter != "" {
				mode = strings.ToLower(historyModeLabel(data.ModeFilter)) + " history"
			}
			return strings.Join(append(rows, st.Dim.Render(fmt.Sprintf("No matches for %q in %s.", query, mode))), "\n")
		}
		if data.ModeFilter != "" {
			return strings.Join(append(rows, st.Dim.Render(fmt.Sprintf("No %s history in %s.", strings.ToLower(historyModeLabel(data.ModeFilter)), historySectionLabel(data.ActiveTab)))), "\n")
		}
		if data.ActiveTab == HistoryTabFinished && data.ContinueCount > 0 {
			return strings.Join(append(rows, st.Dim.Render("No finished history yet.")), "\n")
		}
		if data.ActiveTab == HistoryTabContinue && data.FinishedCount > 0 {
			return strings.Join(append(rows, st.Dim.Render("No titles to continue.")), "\n")
		}
		return strings.Join(append(rows, st.Dim.Render("Nothing here yet. Play something and it shows up.")), "\n")
	}

	showPosterPane := layoutData.Width >= 100 && strings.TrimSpace(layoutData.PosterBlock) != ""
	if !showPosterPane {
		list := renderHistoryList(layoutData, layoutData.Width, st)
		return strings.Join(append(rows, list), "\n")
	}

	rightWidth := max(posterPlaceholderWidth, layoutData.Width/4)
	const paneGap = 6
	leftWidth := layoutData.Width - rightWidth - paneGap
	left := renderHistoryList(layoutData, leftWidth, st)
	right := renderHistoryDetails(layoutData, rightWidth, st)
	joined := lipgloss.JoinHorizontal(
		lipgloss.Top,
		lipgloss.NewStyle().Width(leftWidth).Render(left),
		strings.Repeat(" ", paneGap),
		lipgloss.NewStyle().Width(rightWidth).Render(right),
	)
	return strings.Join(append(rows, joined), "\n")
}

func renderHistoryFilterLine(data HistoryData, width int, st Styles) string {
	if !data.Filtering {
		line := fmt.Sprintf("Filter  %q · %d matches · / edit · esc clear", strings.TrimSpace(data.FilterQuery), len(data.Groups))
		return st.Dim.Render(truncate(line, width))
	}

	label := "Filter / "
	hint := "  enter apply · esc close"
	if width < 70 {
		hint = "  enter · esc close"
	}
	if width < 48 {
		hint = ""
	}
	query := strings.TrimSpace(data.FilterQuery)
	queryStyle := st.Current
	if query == "" {
		query = "type title, episode, or mode"
		queryStyle = st.Dim
	}
	queryWidth := max(1, width-lipgloss.Width(label)-lipgloss.Width(hint)-1)
	return st.Dim.Render(label) + queryStyle.Render(truncate(query, queryWidth)) + st.Cursor.Render("▌") + st.Dim.Render(hint)
}

func renderHistoryNavigation(data HistoryData) string {
	st := NewStyles(data.Accent)
	continueLabel := fmt.Sprintf("Continue %d", data.ContinueCount)
	finishedLabel := fmt.Sprintf("Finished %d", data.FinishedCount)
	if data.ActiveTab == HistoryTabContinue {
		continueLabel = st.ActiveTab.Render("[ " + continueLabel + " ]")
		finishedLabel = st.InactiveTab.Render(finishedLabel)
	} else {
		continueLabel = st.InactiveTab.Render(continueLabel)
		finishedLabel = st.ActiveTab.Render("[ " + finishedLabel + " ]")
	}

	modeLabel := "All"
	modeCount := data.ContinueCount
	if data.ActiveTab == HistoryTabFinished {
		modeCount = data.FinishedCount
	}
	for _, mode := range data.ModeFilters {
		if mode.Mode == data.ModeFilter {
			modeLabel = mode.Label
			modeCount = mode.Count
			break
		}
	}

	sections := strings.Join([]string{
		continueLabel,
		finishedLabel,
	}, "   ")
	mode := st.Dim.Render("Mode") + "  " + st.Current.Render(fmt.Sprintf("[ %s · %d ]", modeLabel, modeCount))
	if lipgloss.Width(sections)+3+lipgloss.Width(mode) > data.Width {
		return sections + "\n" + mode
	}
	return sections + "   " + mode
}

func historySectionLabel(tab HistoryTab) string {
	if tab == HistoryTabFinished {
		return "Finished"
	}
	return "Continue"
}

func historyModeLabel(mode provider.ContentType) string {
	switch mode {
	case provider.ModeAnime:
		return "Anime"
	case provider.ModeCartoon:
		return "Cartoon"
	case provider.ModeLive:
		return "Live"
	case provider.ModeManga:
		return "Manga"
	case provider.ModeMovies:
		return "Movies"
	case provider.ModeTV:
		return "TV"
	case provider.ModeJellyfin:
		return "Jellyfin"
	default:
		return "Other"
	}
}

func historyModes() []provider.ContentType {
	return []provider.ContentType{
		provider.ModeAnime,
		provider.ModeCartoon,
		provider.ModeLive,
		provider.ModeManga,
		provider.ModeMovies,
		provider.ModeTV,
		provider.ModeJellyfin,
	}
}

func historyModeForGroup(group history.Group) provider.ContentType {
	entry := group.ContinueEntry
	if entry.Mode == "" {
		entry.Mode = group.Mode
	}
	if entry.MediaType == "" {
		entry.MediaType = group.MediaType
	}
	return modeForHistoryEntry(entry)
}

func filterHistoryGroups(groups []history.Group, mode provider.ContentType) []history.Group {
	if mode == "" {
		return groups
	}
	filtered := make([]history.Group, 0, len(groups))
	for _, group := range groups {
		if historyModeForGroup(group) == mode {
			filtered = append(filtered, group)
		}
	}
	return filtered
}

func searchHistoryGroups(groups []history.Group, query string) []history.Group {
	query = strings.TrimSpace(query)
	if query == "" {
		return groups
	}
	query = strings.ToLower(query)
	filtered := make([]history.Group, 0, len(groups))
	for _, group := range groups {
		entry := group.ContinueEntry
		if entry.Title == "" {
			entry = group.LastPlayed
		}
		searchable := strings.ToLower(strings.Join([]string{
			group.Title,
			entry.EpisodeTitle,
			historyModeLabel(historyModeForGroup(group)),
		}, " "))
		if strings.Contains(searchable, query) {
			filtered = append(filtered, group)
		}
	}
	return filtered
}

func historyModeFilters(allGroups, activeGroups []history.Group) []HistoryModeFilter {
	if len(allGroups) == 0 {
		return nil
	}
	present := make(map[provider.ContentType]struct{}, len(allGroups))
	counts := make(map[provider.ContentType]int, len(activeGroups))
	for _, group := range allGroups {
		present[historyModeForGroup(group)] = struct{}{}
	}
	for _, group := range activeGroups {
		counts[historyModeForGroup(group)]++
	}
	filters := []HistoryModeFilter{{Label: "All", Count: len(activeGroups)}}
	for _, mode := range historyModes() {
		if _, ok := present[mode]; !ok {
			continue
		}
		filters = append(filters, HistoryModeFilter{
			Mode:  mode,
			Label: historyModeLabel(mode),
			Count: counts[mode],
		})
	}
	return filters
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

	return strings.Join(applyWindow(rows, selPos, data.Height, 0, st), "\n")
}

func renderHistoryRow(g history.Group, _ int, isFocused bool, data HistoryData, width int, st Styles) string {
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

	mode := strings.ToLower(historyModeLabel(historyModeForGroup(g)))
	status := "done"
	if data.ActiveTab != HistoryTabFinished {
		status = historyRowPosition(entry)
	}
	meta := historyRowMetadata(mode, status, relativeTime(entry.WatchedAt), width)
	metaWidth := min(38, max(12, width/2))
	if width >= 72 {
		metaWidth = 42
	}
	titleWidth := max(8, width-metaWidth-4)
	titleTrunc := truncate(title, titleWidth)
	titlePadding := strings.Repeat(" ", max(0, titleWidth-lipgloss.Width(titleTrunc)))
	if isFocused {
		titleTrunc = st.Current.Render(titleTrunc)
	} else {
		titleTrunc = lipgloss.NewStyle().Foreground(colorText).Render(titleTrunc)
	}
	return cursor + titleTrunc + titlePadding + "  " + st.Dim.Render(meta)
}

func historyRowMetadata(mode, status, age string, width int) string {
	if width >= 72 {
		return fmt.Sprintf("%8s  %-20s  %10s", truncate(mode, 8), truncate(status, 20), truncate(age, 10))
	}
	metaWidth := min(38, max(12, width/2))
	meta := truncate(strings.Join([]string{mode, status, age}, " · "), metaWidth)
	return strings.Repeat(" ", max(0, metaWidth-lipgloss.Width(meta))) + meta
}

func historyRowPosition(entry history.Entry) string {
	position := formatContinuePosition(entry)
	if entry.Season > 0 && entry.Episode > 0 && entry.DurationSecs > 0 {
		return fmt.Sprintf("%s · %s/%s", position, formatDurationShort(entry.PositionSecs), formatDurationShort(entry.DurationSecs))
	}
	return position
}

func renderHistoryDetails(data HistoryData, width int, st Styles) string {
	if data.SelectedIndex < 0 || data.SelectedIndex >= len(data.Groups) {
		return ""
	}
	group := data.Groups[data.SelectedIndex]
	entry := group.ContinueEntry
	if data.ActiveTab == HistoryTabFinished {
		entry = group.FarthestComplete
	}
	title := group.Title
	if title == "" {
		title = entry.Title
	}
	rows := []string{
		st.Dim.Render("Selected title"),
		st.Bold.Render(truncate(title, width)),
	}
	if data.PosterBlock != "" {
		rows = append(rows, "", data.PosterBlock)
	}
	return strings.Join(rows, "\n")
}

func renderInlineConfirm(prompt string, _ int, st Styles) string {
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
