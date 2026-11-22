// Package tui owns the terminal interface and its interaction state.
package tui

import (
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// scrollMinInterval is the production port of DEMO 3's MinInterval interval:
// one accepted scroll per 60fps frame.
const scrollMinInterval = 16 * time.Millisecond

type scrollDirection uint8

const (
	scrollDirectionNone scrollDirection = iota
	scrollDirectionUp
	scrollDirectionDown
)

// scrollGate holds the time of the last ACCEPTED scroll. Dropped events leave
// it untouched — advancing it on drops would self-latch after the first row
// and swallow every subsequent flick.
type scrollGate struct {
	lastAcceptedAt time.Time
}

// FilterScrollInput translates wheel events into navigation keys, drops
// repeats that cannot move the cursor at a list boundary, then applies the
// min-interval gate. The guard runs first so focused text inputs are never
// filtered, and the boundary check runs after wheel translation so native
// wheel events get the same treatment as keyboard navigation. Installed
// app-wide via tea.WithFilter.
func FilterScrollInput(model tea.Model, msg tea.Msg) tea.Msg {
	m, ok := model.(*modelImpl)
	if !ok || !m.scrollInputActive() {
		return msg
	}
	// Translate wheel events only for scrollable surfaces; over a focused
	// text input the raw mouse message passes through untouched.
	if wheelMsg, ok := msg.(tea.MouseMsg); ok {
		switch wheelMsg.Button {
		case tea.MouseButtonWheelUp:
			msg = tea.KeyMsg{Type: tea.KeyUp}
		case tea.MouseButtonWheelDown:
			msg = tea.KeyMsg{Type: tea.KeyDown}
		default:
			return msg
		}
	}
	msg = FilterRedundantNavigation(model, msg)
	if msg == nil {
		return nil
	}
	return m.filterScrollInputAt(msg, time.Now())
}

// filterScrollInputAt is the pure, time-injectable core of FilterScrollInput.
// It mirrors DEMO 3's MinInterval.OnScroll: accept when there is no prior
// accept or the interval has elapsed; otherwise drop without touching state.
func (m *modelImpl) filterScrollInputAt(msg tea.Msg, now time.Time) tea.Msg {
	if scrollInputDirection(m, msg) == scrollDirectionNone {
		return msg
	}
	if m.scroll.lastAcceptedAt.IsZero() || now.Sub(m.scroll.lastAcceptedAt) >= scrollMinInterval {
		m.scroll.lastAcceptedAt = now
		return msg
	}
	return nil
}

// resetScrollGate clears the gate on view/mode changes so a new screen never
// starts with a stale acceptance time suppressing its first scroll.
func (m *modelImpl) resetScrollGate() {
	m.scroll = scrollGate{}
}

func (m *modelImpl) scrollInputActive() bool {
	if m.animeStatusPickerOpen || m.addonInputOpen || m.editingAccentHex {
		return false
	}
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

// scrollInputDirection maps a message to its navigation direction, or
// scrollDirectionNone when
// the message is not a scroll key. j/k and g/G are deliberately excluded: they
// are text while a filter input is active, and single presses never carry
// momentum. Wheel events are translated into up/down keys by FilterScrollInput.
func scrollInputDirection(m *modelImpl, msg tea.Msg) scrollDirection {
	keyMsg, ok := msg.(tea.KeyMsg)
	if !ok {
		return scrollDirectionNone
	}
	switch keyMsg.String() {
	case "up", "pgup":
		return scrollDirectionUp
	case "down", "pgdown":
		return scrollDirectionDown
	case "ctrl+u":
		if m.activeView == viewHistory && m.historyFiltering {
			return scrollDirectionNone
		}
		return scrollDirectionUp
	case "ctrl+d":
		if m.activeView == viewEpisodes && m.selectMode {
			return scrollDirectionNone
		}
		return scrollDirectionDown
	}
	return scrollDirectionNone
}
