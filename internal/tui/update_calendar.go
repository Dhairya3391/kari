package tui

import (
	"time"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"kari/internal/calendar"
	"kari/internal/history"
)

func (m *modelImpl) openCalendar() tea.Cmd {
	m.pushView(viewCalendar)
	m.clearStatus()

	if !m.calendarLoaded || len(m.calendarSchedule.Days) == 0 {
		m.loading = true
		m.loadingText = "Loading release calendar..."
		return tea.Batch(m.spinner.Tick, m.fetchCalendarCmd(false))
	}

	// Refresh history cross-referencing on existing schedule
	m.refreshCalendarHistory()
	return nil
}

func (m *modelImpl) fetchCalendarCmd(forceRefresh bool) tea.Cmd {
	opID := m.newOpID()
	m.calendarOpID = opID

	return func() tea.Msg {
		if m.calendarClient == nil {
			return calendarLoadedMsg{opID: opID, err: nil}
		}

		now := time.Now()
		weekStart := calendar.StartOfWeek(now)

		releases, err := m.calendarClient.FetchWeekReleases(m.appCtx, weekStart, forceRefresh)
		if err != nil {
			return calendarLoadedMsg{opID: opID, err: err}
		}

		schedule := calendar.BuildWeeklySchedule(releases, m.historyStore, weekStart, now)
		return calendarLoadedMsg{
			schedule: schedule,
			opID:     opID,
			err:      nil,
		}
	}
}

func (m *modelImpl) refreshCalendarHistory() {
	if len(m.calendarSchedule.Days) == 0 {
		return
	}

	var allReleases []calendar.Release
	for _, d := range m.calendarSchedule.Days {
		for _, item := range d.Releases {
			allReleases = append(allReleases, item.Release)
		}
	}

	now := time.Now()
	weekStart := m.calendarSchedule.Days[0].Date
	m.calendarSchedule = calendar.BuildWeeklySchedule(allReleases, m.historyStore, weekStart, now)
}
func (m *modelImpl) onCalendarLoaded(msg calendarLoadedMsg) (tea.Model, tea.Cmd) {
	if msg.opID != m.calendarOpID {
		return m, nil
	}
	m.loading = false
	m.loadingText = ""
	if msg.err != nil {
		m.setStatus(statusError, "Failed to load schedule: "+msg.err.Error())
		return m, nil
	}
	m.calendarSchedule = msg.schedule
	m.calendarLoaded = true

	// Find today's index in the schedule
	for i, day := range m.calendarSchedule.Days {
		if day.IsToday {
			m.calendarDayIndex = i
			break
		}
	}
	return m, nil
}

func (m *modelImpl) updateCalendar(msg tea.Msg) (tea.Model, tea.Cmd) {
	keyMsg, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}

	if m.calendarFiltering {
		switch keyMsg.String() {
		case "enter":
			m.calendarFiltering = false
			return m, nil
		case "esc":
			m.calendarFiltering = false
			m.calendarQuery = ""
			m.calendarReleaseIndex = 0
			return m, nil
		case "backspace", "ctrl+h":
			if len(m.calendarQuery) > 0 {
				m.calendarQuery = removeLastRune(m.calendarQuery)
				m.calendarReleaseIndex = 0
			}
			return m, nil
		case "ctrl+u":
			m.calendarQuery = ""
			m.calendarReleaseIndex = 0
			return m, nil
		}

		if keyMsg.Type == tea.KeyRunes && len(keyMsg.Runes) > 0 {
			m.calendarQuery += string(keyMsg.Runes)
			m.calendarReleaseIndex = 0
			return m, nil
		}
		if keyMsg.Type == tea.KeySpace {
			m.calendarQuery += " "
			m.calendarReleaseIndex = 0
			return m, nil
		}
		return m, nil
	}

	filtered := m.currentFilteredCalendarReleases()

	switch {
	case key.Matches(keyMsg, m.keys.Back):
		if m.calendarQuery != "" {
			m.calendarQuery = ""
			m.calendarReleaseIndex = 0
			return m, nil
		}
		m.goBackOne()
		return m, nil

	case keyMsg.String() == "up" || keyMsg.String() == "k":
		if m.calendarReleaseIndex > 0 {
			m.calendarReleaseIndex--
		}
		return m, nil

	case keyMsg.String() == "down" || keyMsg.String() == "j":
		if m.calendarReleaseIndex < len(filtered)-1 {
			m.calendarReleaseIndex++
		}
		return m, nil
	case keyMsg.String() == "g" || keyMsg.String() == "home":
		m.calendarReleaseIndex = 0
		return m, nil

	case keyMsg.String() == "G" || keyMsg.String() == "end":
		if len(filtered) > 0 {
			m.calendarReleaseIndex = len(filtered) - 1
		}
		return m, nil

	case keyMsg.String() == "pgup" || keyMsg.String() == "ctrl+u":
		m.calendarReleaseIndex = max(0, m.calendarReleaseIndex-10)
		return m, nil

	case keyMsg.String() == "pgdown" || keyMsg.String() == "ctrl+d":
		if len(filtered) > 0 {
			m.calendarReleaseIndex = min(len(filtered)-1, m.calendarReleaseIndex+10)
		}
		return m, nil

	case keyMsg.String() == "left" || keyMsg.String() == "h":
		if len(m.calendarSchedule.Days) > 0 {
			m.calendarDayIndex = (m.calendarDayIndex + len(m.calendarSchedule.Days) - 1) % len(m.calendarSchedule.Days)
			m.calendarReleaseIndex = 0
			m.calendarScrollOffset = 0
		}
		return m, nil

	case keyMsg.String() == "right" || keyMsg.String() == "l":
		if len(m.calendarSchedule.Days) > 0 {
			m.calendarDayIndex = (m.calendarDayIndex + 1) % len(m.calendarSchedule.Days)
			m.calendarReleaseIndex = 0
			m.calendarScrollOffset = 0
		}
		return m, nil

	case key.Matches(keyMsg, m.keys.Type) || keyMsg.String() == "tab":
		m.cycleCalendarFilter(false)
		return m, nil

	case keyMsg.Type == tea.KeyShiftTab:
		m.cycleCalendarFilter(true)
		return m, nil

	case keyMsg.String() == "1":
		m.calendarFilter = calendar.FilterAll
		m.calendarReleaseIndex = 0
		return m, nil

	case keyMsg.String() == "2":
		m.calendarFilter = calendar.FilterAnime
		m.calendarReleaseIndex = 0
		return m, nil

	case keyMsg.String() == "3":
		m.calendarFilter = calendar.FilterSeries
		m.calendarReleaseIndex = 0
		return m, nil

	case keyMsg.String() == "4":
		m.calendarFilter = calendar.FilterWatching
		m.calendarReleaseIndex = 0
		return m, nil

	case keyMsg.String() == "/":
		m.calendarFiltering = true
		return m, nil

	case key.Matches(keyMsg, m.keys.Restart) || keyMsg.String() == "r":
		m.loading = true
		m.loadingText = "Refreshing release calendar..."
		return m, tea.Batch(m.spinner.Tick, m.fetchCalendarCmd(true))

	case key.Matches(keyMsg, m.keys.Select) || key.Matches(keyMsg, m.keys.Play) || keyMsg.String() == "enter":
		if len(filtered) == 0 || m.calendarReleaseIndex >= len(filtered) {
			return m, nil
		}
		item := filtered[m.calendarReleaseIndex]
		return m.activateCalendarRelease(item)
	}

	return m, nil
}

func (m *modelImpl) cycleCalendarFilter(reverse bool) {
	filters := []calendar.FilterMode{
		calendar.FilterAll,
		calendar.FilterAnime,
		calendar.FilterSeries,
		calendar.FilterWatching,
	}

	currentIdx := 0
	for i, f := range filters {
		if f == m.calendarFilter {
			currentIdx = i
			break
		}
	}

	if reverse {
		currentIdx = (currentIdx + len(filters) - 1) % len(filters)
	} else {
		currentIdx = (currentIdx + 1) % len(filters)
	}

	m.calendarFilter = filters[currentIdx]
	m.calendarReleaseIndex = 0
}

func (m *modelImpl) currentFilteredCalendarReleases() []calendar.ReleaseItem {
	if len(m.calendarSchedule.Days) == 0 || m.calendarDayIndex < 0 || m.calendarDayIndex >= len(m.calendarSchedule.Days) {
		return nil
	}
	day := m.calendarSchedule.Days[m.calendarDayIndex]
	return filterReleases(day.Releases, m.calendarFilter, m.calendarQuery)
}

func (m *modelImpl) activateCalendarRelease(item calendar.ReleaseItem) (tea.Model, tea.Cmd) {
	// If the show is in history and has a known next episode, resume directly
	if item.InHistory && item.NextEpisode > 0 {
		entry := history.Entry{
			Title:     item.Release.Title,
			Mode:      string(item.Release.Mode),
			MediaType: string(item.Release.Mode),
			Season:    item.Release.Season,
			Episode:   item.NextEpisode,
			AniListID: item.Release.AniListID,
			TMDBID:    item.Release.TMDBID,
		}
		return m.resumeHistoryEntry(entry)
	}

	// Otherwise, switch to Search mode and execute search for this title
	m.appMode = item.Release.Mode
	m.queryInput.SetValue(item.Release.Title)
	m.activeView = viewSearch
	m.backStack = nil
	m.clearStatus()
	return m.startSearchFromInput()
}

func (m *modelImpl) collectCalendarData(width, height int, accent lipgloss.AdaptiveColor) CalendarData {
	if m.calendarFilter == "" {
		m.calendarFilter = calendar.FilterAll
	}

	return CalendarData{
		Schedule:      m.calendarSchedule,
		DayIndex:      m.calendarDayIndex,
		SelectedIndex: m.calendarReleaseIndex,
		ScrollOffset:  m.calendarScrollOffset,
		Filter:        m.calendarFilter,
		FilterQuery:   m.calendarQuery,
		Filtering:     m.calendarFiltering,
		Width:         width,
		Height:        height,
		Accent:        accent,
		Loading:       m.loading,
	}
}
