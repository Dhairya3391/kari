package tui

import (
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

const scrollInputCoalesceWindow = 8 * time.Millisecond

// FilterScrollInput coalesces rapid same-direction navigation bursts so
// buffered trackpad input does not keep moving a list after the gesture ends.
func FilterScrollInput(model tea.Model, msg tea.Msg) tea.Msg {
	msg = FilterRedundantEpisodeNavigation(model, msg)
	if msg == nil {
		return nil
	}
	m, ok := model.(*modelImpl)
	if !ok || !m.scrollInputActive() {
		return msg
	}
	direction := scrollInputDirection(m, msg)
	if direction == "" {
		if _, ok := msg.(tea.KeyMsg); ok {
			m.lastScrollDirection = ""
		}
		return msg
	}

	now := time.Now()
	if direction == m.lastScrollDirection && now.Sub(m.lastScrollInputAt) < scrollInputCoalesceWindow {
		return nil
	}
	m.lastScrollDirection = direction
	m.lastScrollInputAt = now
	return msg
}

func (m *modelImpl) scrollInputActive() bool {
	if m.showHelp {
		return true
	}
	switch m.activeView {
	case viewSearch:
		return !m.queryInput.Focused() && len(m.seriesList.Items()) > 0
	case viewEpisodes:
		return m.hasCurrentEpisodeProjection()
	case viewChapters, viewReader, viewHistory, viewDownloads, viewPreview:
		return true
	case viewCalendar:
		return !m.calendarFiltering
	case viewSettings:
		return m.audioPickerOpen || !m.authInput.Focused()
	default:
		return false
	}
}

func scrollInputDirection(m *modelImpl, msg tea.Msg) string {
	if keyMsg, ok := msg.(tea.KeyMsg); ok {
		switch keyMsg.String() {
		case "up", "pgup":
			return "up"
		case "down", "pgdown":
			return "down"
		case "ctrl+u":
			if m.activeView == viewHistory && m.historyFiltering {
				return ""
			}
			return "up"
		case "ctrl+d":
			if m.activeView == viewEpisodes && m.selectMode {
				return ""
			}
			return "down"
		}
		return ""
	}
	if mouseMsg, ok := msg.(tea.MouseMsg); ok {
		switch mouseMsg.Button {
		case tea.MouseButtonWheelUp:
			return "up"
		case tea.MouseButtonWheelDown:
			return "down"
		}
	}
	return ""
}
