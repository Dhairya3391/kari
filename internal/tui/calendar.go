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
	ScrollOffset  int
	Filter        calendar.FilterMode
	FilterQuery   string
	Filtering     bool
	Width         int
	Height        int
	Accent        lipgloss.AdaptiveColor
	Loading       bool
}

// RenderCalendarScreen renders the weekly release schedule with unviewed episode tracking.
func RenderCalendarScreen(data CalendarData) string {
	st := NewStyles(data.Accent)
	data.Width = min(data.Width, 140)

	if data.Loading && len(data.Schedule.Days) == 0 {
		return st.Dim.Render("Loading release calendar...")
	}

	if len(data.Schedule.Days) == 0 {
		return st.Dim.Render("No release schedule available. Press 'r' to refresh.")
	}

	if data.DayIndex < 0 || data.DayIndex >= len(data.Schedule.Days) {
		data.DayIndex = 0
	}

	activeDay := data.Schedule.Days[data.DayIndex]

	// 1. Navigation / Days Bar
	daysBar := renderCalendarDaysBar(data, st)

	// 2. Filter Bar
	filterBar := renderCalendarFilterBar(data, activeDay, st)

	rows := []string{daysBar, "", filterBar, ""}
	headerHeight := lipgloss.Height(strings.Join(rows, "\n"))

	// 3. Search / Filter line if typing
	if data.Filtering || strings.TrimSpace(data.FilterQuery) != "" {
		filterLine := renderCalendarFilterLine(data, data.Width, st)
		rows = append(rows, filterLine, "")
		headerHeight += 2
	}

	bodyHeight := max(5, data.Height-headerHeight)

	// Filter releases for the selected day
	filtered := filterReleases(activeDay.Releases, data.Filter, data.FilterQuery)

	if len(filtered) == 0 {
		emptyMsg := "No scheduled releases found for this day."
		if data.Filter == calendar.FilterWatching {
			emptyMsg = fmt.Sprintf("None of the shows you're watching air on %s.", activeDay.DayName)
		} else if data.FilterQuery != "" {
			emptyMsg = fmt.Sprintf("No releases matching %q on %s.", data.FilterQuery, activeDay.DayName)
		}
		rows = append(rows, st.Dim.Render(emptyMsg))
		return strings.Join(rows, "\n")
	}

	// Clamp selected index
	if data.SelectedIndex >= len(filtered) {
		data.SelectedIndex = max(0, len(filtered)-1)
	}

	showDetailPane := data.Width >= 100
	if !showDetailPane {
		listLines := renderCalendarList(data, filtered, data.Width, bodyHeight, st)
		return strings.Join(append(rows, listLines...), "\n")
	}

	rightWidth := max(34, data.Width/3)
	const paneGap = 4
	leftWidth := data.Width - rightWidth - paneGap

	listLines := renderCalendarList(data, filtered, leftWidth, bodyHeight, st)
	detailBlock := renderCalendarDetails(filtered[data.SelectedIndex], rightWidth, bodyHeight, st)

	leftCol := strings.Join(listLines, "\n")
	joined := lipgloss.JoinHorizontal(
		lipgloss.Top,
		lipgloss.NewStyle().Width(leftWidth).Render(leftCol),
		strings.Repeat(" ", paneGap),
		lipgloss.NewStyle().Width(rightWidth).Render(detailBlock),
	)

	return strings.Join(append(rows, joined), "\n")
}

func renderCalendarDaysBar(data CalendarData, st Styles) string {
	var parts []string
	for i, day := range data.Schedule.Days {
		label := day.ShortDayName + " " + day.Date.Format("Jan 02")
		if day.IsToday {
			label += " (Today)"
		}

		if i == data.DayIndex {
			parts = append(parts, st.ActiveTab.Render("[ "+label+" ]"))
		} else if day.IsToday {
			parts = append(parts, st.Current.Render(label))
		} else {
			parts = append(parts, st.InactiveTab.Render(label))
		}
	}
	return strings.Join(parts, "   ")
}

func renderCalendarFilterBar(data CalendarData, day calendar.DaySchedule, st Styles) string {
	allCount := len(day.Releases)
	animeCount := 0
	seriesCount := 0
	watchingCount := 0

	for _, r := range day.Releases {
		switch r.Release.Mode {
		case provider.ModeAnime:
			animeCount++
		case provider.ModeTV:
			seriesCount++
		}
		if r.InHistory {
			watchingCount++
		}
	}

	filters := []struct {
		mode  calendar.FilterMode
		label string
	}{
		{calendar.FilterAll, fmt.Sprintf("All (%d)", allCount)},
		{calendar.FilterAnime, fmt.Sprintf("Anime (%d)", animeCount)},
		{calendar.FilterSeries, fmt.Sprintf("Series (%d)", seriesCount)},
		{calendar.FilterWatching, fmt.Sprintf("Watching (%d)", watchingCount)},
	}

	var parts []string
	for _, f := range filters {
		if data.Filter == f.mode {
			parts = append(parts, st.ActiveTab.Render("[ "+f.label+" ]"))
		} else {
			parts = append(parts, st.InactiveTab.Render(f.label))
		}
	}

	hint := st.Dim.Render("   (←/→ day · tab filter · enter watch/search · r refresh)")
	return strings.Join(parts, "  ") + hint
}

func renderCalendarFilterLine(data CalendarData, width int, st Styles) string {
	if !data.Filtering {
		line := fmt.Sprintf("Filter  %q · / edit · esc clear", strings.TrimSpace(data.FilterQuery))
		return st.Dim.Render(truncate(line, width))
	}

	label := "Filter / "
	query := strings.TrimSpace(data.FilterQuery)
	queryStyle := st.Current
	if query == "" {
		query = "type title to filter..."
		queryStyle = st.Dim
	}
	hint := "  enter apply · esc close"
	return st.Dim.Render(label) + queryStyle.Render(query) + st.Cursor.Render("▌") + st.Dim.Render(hint)
}

func renderCalendarList(data CalendarData, items []calendar.ReleaseItem, width, height int, st Styles) []string {
	if len(items) == 0 {
		return []string{st.Dim.Render("No releases.")}
	}

	startIdx := windowStart(len(items), data.SelectedIndex, height)
	endIdx := min(len(items), startIdx+height)
	lines := make([]string, 0, height)
	for i := startIdx; i < endIdx; i++ {
		item := items[i]
		isSelected := (i == data.SelectedIndex)

		line := renderCalendarRow(item, isSelected, width, st)
		lines = append(lines, line)
	}

	// Fill remaining height to preserve geometry
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

	// 1. Time string
	airTime := item.Release.AiringAt.Local().Format("15:04")
	timeStr := st.Dim.Render(fmt.Sprintf("%-5s", airTime))

	// 2. Mode tag
	modeTag := "[ani]"
	if item.Release.Mode == provider.ModeTV {
		modeTag = "[tv] "
	}
	modeStr := st.Dim.Render(modeTag)

	// 3. Status Badge
	badgeStr := renderWatchBadge(item, st)

	// 4. Episode label
	epLabel := fmt.Sprintf("Ep %-3d", item.Release.Episode)
	if item.Release.Mode == provider.ModeTV && item.Release.Season > 0 {
		epLabel = fmt.Sprintf("S%02dE%02d", item.Release.Season, item.Release.Episode)
	}
	epStr := st.Dim.Render(epLabel)

	// Fixed width elements: cursor (2) + time (5) + space (1) + mode (5) + space (1) + ep (6) + space (2) + badge (~22)
	badgeWidth := lipgloss.Width(badgeStr)
	fixedWidth := 2 + 5 + 1 + 5 + 1 + 6 + 2 + badgeWidth
	titleAvail := max(15, width-fixedWidth)

	title := truncate(item.Release.Title, titleAvail)
	titleStr := title
	if isSelected {
		titleStr = st.Current.Render(title)
	} else if item.InHistory {
		titleStr = lipgloss.NewStyle().Bold(true).Render(title)
	}

	gap := max(1, width-lipgloss.Width(cursor)-lipgloss.Width(timeStr)-1-lipgloss.Width(modeStr)-1-lipgloss.Width(titleStr)-2-lipgloss.Width(epStr))
	if badgeWidth > 0 {
		gap = max(1, gap-2-badgeWidth)
		return cursor + timeStr + " " + modeStr + " " + titleStr + strings.Repeat(" ", gap) + epStr + "  " + badgeStr
	}
	return cursor + timeStr + " " + modeStr + " " + titleStr + strings.Repeat(" ", gap) + epStr
}

func renderWatchBadge(item calendar.ReleaseItem, _ Styles) string {
	switch item.Status {
	case calendar.StatusBehind:
		// Highlighted bold badge: viewer is behind on episodes
		return lipgloss.NewStyle().
			Foreground(lipgloss.AdaptiveColor{Light: "#B45309", Dark: "#F59E0B"}).
			Bold(true).
			Render(item.Badge)

	case calendar.StatusNewToday:
		// High-contrast accent: viewer was caught up, new episode today
		return lipgloss.NewStyle().
			Foreground(lipgloss.AdaptiveColor{Light: "#047857", Dark: "#10B981"}).
			Bold(true).
			Render(item.Badge)

	case calendar.StatusUpToDate:
		// Dim muted green: already watched
		return lipgloss.NewStyle().
			Foreground(lipgloss.AdaptiveColor{Light: "#6B7280", Dark: "#9CA3AF"}).
			Render(item.Badge)

	default:
		return ""
	}
}

func renderCalendarDetails(item calendar.ReleaseItem, width, height int, st Styles) string {
	rel := item.Release

	var b strings.Builder

	// Header: Title
	b.WriteString(st.Current.Render(truncate(rel.Title, width)))
	b.WriteString("\n")

	if rel.RomajiTitle != "" && rel.RomajiTitle != rel.Title {
		b.WriteString(st.Dim.Render(truncate(rel.RomajiTitle, width)))
		b.WriteString("\n")
	}

	// Episode / Season info
	epInfo := fmt.Sprintf("Episode %d", rel.Episode)
	if rel.Mode == provider.ModeTV && rel.Season > 0 {
		epInfo = fmt.Sprintf("Season %d, Episode %d", rel.Season, rel.Episode)
	}
	if rel.EpisodeTitle != "" {
		epInfo += " · " + rel.EpisodeTitle
	}
	b.WriteString(st.Current.Render(truncate(epInfo, width)))
	b.WriteString("\n\n")

	// Airing Time and Relative Countdown
	airTimeLocal := rel.AiringAt.Local()
	airStr := airTimeLocal.Format("Monday, Jan 02 at 15:04")
	now := time.Now()
	if rel.AiringAt.After(now) {
		remaining := time.Until(rel.AiringAt).Round(time.Minute)
		hours := int(remaining.Hours())
		mins := int(remaining.Minutes()) % 60
		airStr += fmt.Sprintf(" (in %dh %dm)", hours, mins)
	} else {
		airStr += " (Aired)"
	}
	b.WriteString(st.Dim.Render("Broadcast: "))
	b.WriteString(airStr)
	b.WriteString("\n\n")

	// Watch Progress Card
	b.WriteString(st.Wordmark.Render("WATCH PROGRESS"))
	b.WriteString("\n")
	b.WriteString(strings.Repeat("─", min(width, 36)))
	b.WriteString("\n")

	if item.InHistory {
		b.WriteString(st.Dim.Render("Status:       "))
		b.WriteString(st.Current.Render("Currently Watching"))
		b.WriteString("\n")
		if item.LastWatchedEpisode > 0 {
			b.WriteString(st.Dim.Render("Last Watched: "))
			fmt.Fprintf(&b, "Episode %d", item.LastWatchedEpisode)
			b.WriteString("\n")
		}
		if item.UnwatchedCount > 0 {
			b.WriteString(st.Dim.Render("Left to Watch:"))
			b.WriteString(" ")
			b.WriteString(lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "#B45309", Dark: "#F59E0B"}).Bold(true).
				Render(fmt.Sprintf("%d episode(s) (Next: Ep %d)", item.UnwatchedCount, item.NextEpisode)))
			b.WriteString("\n")
		} else {
			b.WriteString(st.Dim.Render("Left to Watch:"))
			b.WriteString(" ")
			b.WriteString(lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "#047857", Dark: "#10B981"}).
				Render("Caught up! Up to date."))
			b.WriteString("\n")
		}
		b.WriteString("\n")
		b.WriteString(st.Current.Render("▶ Press [Enter] to resume Ep " + fmt.Sprintf("%d", item.NextEpisode)))
		b.WriteString("\n")
	} else {
		b.WriteString(st.Dim.Render("Status:       "))
		b.WriteString("Not started\n")
		if rel.TotalEpisodes > 0 {
			b.WriteString(st.Dim.Render("Episodes:     "))
			fmt.Fprintf(&b, "%d total", rel.TotalEpisodes)
			b.WriteString("\n")
		}
		b.WriteString("\n")
		b.WriteString(st.Current.Render("▶ Press [Enter] to search & play"))
		b.WriteString("\n")
	}
	b.WriteString(strings.Repeat("─", min(width, 36)))
	b.WriteString("\n\n")

	// Overview / Synopsis
	if rel.Description != "" {
		b.WriteString(st.Dim.Render("Synopsis:"))
		b.WriteString("\n")
		cleanDesc := truncate(rel.Description, width*4)
		b.WriteString(st.Dim.Render(cleanDesc))
	}

	lines := strings.Split(b.String(), "\n")
	if len(lines) > height {
		lines = lines[:height]
	}
	return strings.Join(lines, "\n")
}

func filterReleases(releases []calendar.ReleaseItem, filter calendar.FilterMode, query string) []calendar.ReleaseItem {
	query = strings.ToLower(strings.TrimSpace(query))
	var result []calendar.ReleaseItem

	for _, item := range releases {
		// Filter by mode
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

		// Filter by query
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
