package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"

	"kari/internal/calendar"
	"kari/internal/provider"
)

// CalendarData carries state needed to render the Calendar screen.
type CalendarData struct {
	Schedule      calendar.WeeklySchedule
	DayIndex      int
	SelectedIndex int
	Filter        calendar.FilterMode
	FilterQuery   string
	Filtering     bool
	Width         int
	Height        int
	Accent        lipgloss.AdaptiveColor
	Loading       bool
	PosterBlock   string
	Now           time.Time
}

const (
	calendarDetailPaneWidth    = 100
	calendarFooterHintCount    = 7
	calendarEpisodeColumnWidth = 8
)

// calendarKeyHints lists every Calendar binding in footer priority order. The
// footer and help overlay both read from this one list.
func calendarKeyHints() []KeyBinding {
	return []KeyBinding{
		{Key: "enter", Action: "resume/search"},
		{Key: "↑/↓", Action: "move"},
		{Key: "←/→", Action: "day"},
		{Key: "tab", Action: "scope"},
		{Key: "/", Action: "search"},
		{Key: "r", Action: "refresh"},
		{Key: "esc", Action: "back"},
		{Key: "1-4", Action: "jump scope"},
		{Key: "g/G", Action: "top/bottom"},
		{Key: "pgup/pgdn", Action: "page"},
	}
}

func calendarFooterHints() []KeyBinding {
	return calendarKeyHints()[:calendarFooterHintCount]
}

// RenderCalendarScreen renders the weekly release schedule with unviewed episode tracking.
func RenderCalendarScreen(data CalendarData) string {
	st := NewStyles(data.Accent)
	if data.Now.IsZero() {
		data.Now = time.Now()
	}
	if data.Width <= 0 {
		data.Width = 1
	}

	if data.Loading && len(data.Schedule.Days) == 0 {
		return st.Dim.Render("Loading release calendar...")
	}
	if len(data.Schedule.Days) == 0 {
		return st.Dim.Render("No release schedule available. Press 'r' to refresh.")
	}
	if data.DayIndex < 0 || data.DayIndex >= len(data.Schedule.Days) {
		data.DayIndex = 0
	}

	rows := []string{
		renderCalendarNav(data, st),
		"",
	}

	bodyHeight := max(5, data.Height-len(rows))
	activeDay := data.Schedule.Days[data.DayIndex]
	filtered := filterReleases(activeDay.Releases, data.Filter, data.FilterQuery)

	if len(filtered) == 0 {
		return strings.Join(append(rows, st.Dim.Render(calendarEmptyMessage(data, activeDay))), "\n")
	}
	if data.SelectedIndex < 0 || data.SelectedIndex >= len(filtered) {
		data.SelectedIndex = max(0, len(filtered)-1)
	}

	if data.Width < calendarDetailPaneWidth {
		list := padLines(renderCalendarList(data, filtered, data.Width, bodyHeight, st), bodyHeight)
		return strings.Join(append(rows, list...), "\n")
	}

	const paneGap = 4
	rightWidth := max(34, data.Width/3)
	leftWidth := data.Width - rightWidth - paneGap
	list := padLines(renderCalendarList(data, filtered, leftWidth, bodyHeight, st), bodyHeight)
	detail := padLines(strings.Split(renderCalendarDetails(filtered[data.SelectedIndex], data, rightWidth, bodyHeight, st), "\n"), bodyHeight)
	joined := lipgloss.JoinHorizontal(
		lipgloss.Top,
		lipgloss.NewStyle().Width(leftWidth).Render(strings.Join(list, "\n")),
		strings.Repeat(" ", paneGap),
		lipgloss.NewStyle().Width(rightWidth).Render(strings.Join(detail, "\n")),
	)
	return strings.Join(append(rows, joined), "\n")
}

func calendarEmptyMessage(data CalendarData, day calendar.DaySchedule) string {
	if data.FilterQuery != "" {
		return fmt.Sprintf("No releases matching %q on %s.", data.FilterQuery, day.DayName)
	}
	if data.Filter == calendar.FilterWatching {
		return fmt.Sprintf("None of the shows you're watching air on %s.", day.DayName)
	}
	return "No scheduled releases for this day."
}

// renderCalendarNav mirrors renderHistoryNavigation: one pill for the active
// day and one for the active scope instead of a row of every option.
func renderCalendarNav(data CalendarData, st Styles) string {
	day := data.Schedule.Days[data.DayIndex]
	dateLabel := day.ShortDayName + " " + day.Date.Format("Jan 02")
	if day.IsToday {
		dateLabel += " ●"
	}
	nav := st.ActiveTab.Render("[ " + dateLabel + " ]")

	scope, count := calendarScopeLabel(day.Releases, data.Filter)
	nav += st.Dim.Render("   Scope  ") +
		st.Current.Render(fmt.Sprintf("[ %s · %d ]", scope, count))

	if data.Filtering {
		query := strings.TrimSpace(data.FilterQuery)
		queryStyle := st.Current
		if query == "" {
			query = "type a title..."
			queryStyle = st.Dim
		}
		nav += st.Dim.Render("   Search: ") + queryStyle.Render(query) +
			st.Cursor.Render("▌") + st.Dim.Render("  enter · esc")
	} else if query := strings.TrimSpace(data.FilterQuery); query != "" {
		nav += st.Dim.Render("   Search: " + query)
	}
	return truncate(nav, data.Width)
}

func calendarScopeLabel(items []calendar.ReleaseItem, filter calendar.FilterMode) (string, int) {
	var anime, series, watching int
	for _, item := range items {
		switch item.Release.Mode {
		case provider.ModeAnime:
			anime++
		case provider.ModeTV:
			series++
		}
		if item.InHistory {
			watching++
		}
	}

	switch filter {
	case calendar.FilterAnime:
		return "Anime", anime
	case calendar.FilterSeries:
		return "Series", series
	case calendar.FilterWatching:
		return "Watching", watching
	default:
		return "All", len(items)
	}
}

func renderCalendarList(data CalendarData, items []calendar.ReleaseItem, width, height int, st Styles) []string {
	if len(items) == 0 {
		return []string{st.Dim.Render("No releases.")}
	}

	startIdx, endIdx, above, below := windowRange(len(items), data.SelectedIndex, height, 0)
	lines := make([]string, 0, height)
	if hint := windowHint(above, true, st); hint != "" {
		lines = append(lines, hint)
	}
	for i := startIdx; i < endIdx; i++ {
		lines = append(lines, renderCalendarRow(items[i], i == data.SelectedIndex, width, st))
	}
	if hint := windowHint(below, false, st); hint != "" {
		lines = append(lines, hint)
	}
	for len(lines) < height {
		lines = append(lines, "")
	}
	return lines
}

func renderCalendarRow(item calendar.ReleaseItem, isSelected bool, width int, st Styles) string {
	cursor := "  "
	if isSelected {
		cursor = st.Cursor.Render("▌ ")
	}

	timeStr := st.Dim.Render(item.Release.AiringAt.Local().Format("15:04"))
	epStr := st.Dim.Render(fmt.Sprintf("%*s", calendarEpisodeColumnWidth, calendarEpisodeCode(item.Release)))
	chip := renderStatusChip(item, st)

	cursorW := lipgloss.Width(cursor)
	timeW := lipgloss.Width(timeStr)
	epW := lipgloss.Width(epStr)
	chipW := lipgloss.Width(chip)
	titleAvail := max(12, width-cursorW-timeW-1-epW-2-chipW)

	title := truncate(item.Release.Title, titleAvail)
	switch {
	case isSelected:
		title = st.Current.Render(title)
	case item.InHistory:
		title = st.Bold.Render(title)
	}

	gap := max(1, width-cursorW-timeW-1-lipgloss.Width(title)-2-epW-chipW)
	return cursor + timeStr + " " + title + strings.Repeat(" ", gap) + epStr + "  " + chip
}

func calendarEpisodeCode(rel calendar.Release) string {
	return calendarEpisodeLabel(rel.Mode, rel.Season, rel.Episode)
}

func calendarEpisodeLabel(mode provider.ContentType, season, episode int) string {
	if mode == provider.ModeTV && season > 0 {
		return fmt.Sprintf("S%02d E%03d", season, episode)
	}
	return fmt.Sprintf("E%03d", episode)
}

func renderStatusChip(item calendar.ReleaseItem, st Styles) string {
	switch item.Status {
	case calendar.StatusNewToday:
		return st.Ok.Render("● new")
	case calendar.StatusBehind:
		return st.Warn.Render(fmt.Sprintf("▸%d", item.UnwatchedCount))
	case calendar.StatusUpToDate:
		return st.Ok.Render("✓")
	default:
		return st.Dim.Render("·")
	}
}

func renderCalendarDetails(item calendar.ReleaseItem, data CalendarData, width, height int, st Styles) string {
	rel := item.Release
	var b strings.Builder

	b.WriteString(st.Current.Render(truncate(rel.Title, width)))
	b.WriteString("\n")
	if rel.RomajiTitle != "" && rel.RomajiTitle != rel.Title {
		b.WriteString(st.Dim.Render(truncate(rel.RomajiTitle, width)))
		b.WriteString("\n")
	}

	epInfo := calendarEpisodeCode(rel)
	if rel.EpisodeTitle != "" {
		epInfo += " · " + rel.EpisodeTitle
	}
	b.WriteString(truncate(epInfo, width))
	b.WriteString("\n\n")

	air := rel.AiringAt.Local().Format("Mon 02 Jan 15:04")
	if rel.AiringAt.After(data.Now) {
		remaining := rel.AiringAt.Sub(data.Now).Round(time.Minute)
		air += fmt.Sprintf(" · in %dh %dm", int(remaining.Hours()), int(remaining.Minutes())%60)
	} else {
		air += " · aired"
	}
	b.WriteString(st.Dim.Render("Broadcast: " + truncate(air, width)))
	b.WriteString("\n\n")

	b.WriteString(st.Wordmark.Render("PROGRESS"))
	b.WriteString("\n")
	rule := st.Dim.Render(strings.Repeat("─", min(width, 36)))
	b.WriteString(rule)
	b.WriteString("\n")

	if item.InHistory {
		b.WriteString(st.Dim.Render("Status:       "))
		b.WriteString(st.Current.Render("Watching"))
		b.WriteString("\n")
		if item.LastWatchedEpisode > 0 {
			b.WriteString(st.Dim.Render("Last watched: "))
			b.WriteString(calendarEpisodeLabel(rel.Mode, rel.Season, item.LastWatchedEpisode))
			b.WriteString("\n")
		}
		b.WriteString(st.Dim.Render("Left to watch: "))
		if item.UnwatchedCount > 0 {
			b.WriteString(st.Warn.Render(fmt.Sprintf("%d · next %s",
				item.UnwatchedCount, calendarEpisodeLabel(rel.Mode, rel.Season, item.NextEpisode))))
		} else {
			b.WriteString(st.Ok.Render("caught up"))
		}
		b.WriteString("\n\n")
		if item.NextEpisode > 0 {
			b.WriteString(st.Current.Render("▶ Enter to resume " +
				calendarEpisodeLabel(rel.Mode, rel.Season, item.NextEpisode)))
			b.WriteString("\n")
		}
	} else {
		b.WriteString(st.Dim.Render("Status:       "))
		b.WriteString("Not started\n")
		if rel.TotalEpisodes > 0 {
			b.WriteString(st.Dim.Render("Episodes:     "))
			fmt.Fprintf(&b, "%d total\n", rel.TotalEpisodes)
		}
		b.WriteString("\n")
		b.WriteString(st.Current.Render("▶ Enter to search & play"))
		b.WriteString("\n")
	}

	b.WriteString(rule)
	b.WriteString("\n\n")

	if rel.Description != "" {
		b.WriteString(st.Dim.Render("Synopsis:"))
		b.WriteString("\n")
		for _, line := range strings.Split(wrapFixed(rel.Description, width, calendarSynopsisRows), "\n") {
			b.WriteString(st.Dim.Render(line))
			b.WriteString("\n")
		}
	}

	lines := padLines(strings.Split(b.String(), "\n"), calendarDetailsMetaRows)
	if data.PosterBlock != "" && calendarPosterFits(height) {
		lines = append(lines, strings.Split(data.PosterBlock, "\n")...)
	}

	return strings.Join(padLines(lines, height), "\n")
}

func filterReleases(releases []calendar.ReleaseItem, filter calendar.FilterMode, query string) []calendar.ReleaseItem {
	query = strings.ToLower(strings.TrimSpace(query))
	var result []calendar.ReleaseItem

	for _, item := range releases {
		switch filter {
		case calendar.FilterAnime:
			if item.Release.Mode != provider.ModeAnime {
				continue
			}
		case calendar.FilterSeries:
			if item.Release.Mode != provider.ModeTV {
				continue
			}
		case calendar.FilterWatching:
			if !item.InHistory {
				continue
			}
		}

		if query != "" {
			titleLower := strings.ToLower(item.Release.Title)
			romajiLower := strings.ToLower(item.Release.RomajiTitle)
			if !strings.Contains(titleLower, query) && !strings.Contains(romajiLower, query) {
				continue
			}
		}

		result = append(result, item)
	}

	return result
}
