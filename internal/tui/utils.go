package tui

import (
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

const (
	maxContentWidth = 118
)

type layoutDims struct {
	contentW int
	bodyW    int
}

func (m *modelImpl) computeLayoutDims() layoutDims {
	contentW := m.width - 4
	if contentW < 36 {
		contentW = max(20, m.width-2)
	}
	if contentW > maxContentWidth {
		contentW = maxContentWidth
	}

	return layoutDims{contentW: contentW, bodyW: contentW}
}

func (m *modelImpl) resizeLists() {
	dims := m.computeLayoutDims()
	w := dims.bodyW - 4
	if w < 24 {
		w = 24
	}
	h := max(10, m.height-13)
	m.seriesList.SetSize(w, h)
	m.episodeList.SetSize(w, h)
	m.historyList.SetSize(w, h)
}

func (m *modelImpl) setStatus(level statusLevel, text string) {
	m.statusType = level
	m.statusID++
	if text == "" {
		m.statusText = ""
		return
	}
	m.statusText = text
}

func (m *modelImpl) clearStatusAfter(d time.Duration) tea.Cmd {
	id := m.statusID
	return tea.Tick(d, func(t time.Time) tea.Msg {
		return resetStatusMsg{id: id}
	})
}

func (m *modelImpl) pushView(next viewState) {
	if m.activeView == next {
		return
	}

	// Prevent duplicate entries in backstack (e.g. going from preview to preview)
	if len(m.backStack) > 0 && m.backStack[len(m.backStack)-1] == next {
		// If we are "going back" but used pushView, just pop instead
		m.activeView = next
		m.backStack = m.backStack[:len(m.backStack)-1]
		return
	}

	m.backStack = append(m.backStack, m.activeView)
	m.activeView = next
}

func (m *modelImpl) goBackOne() bool {
	if len(m.backStack) == 0 {
		return false
	}
	prev := m.backStack[len(m.backStack)-1]
	m.backStack = m.backStack[:len(m.backStack)-1]
	m.activeView = prev
	return true
}

func (m *modelImpl) nextEpisodeIndex() (int, bool) {
	if m.selectedSeries == nil || len(m.episodeResults) == 0 {
		return 0, false
	}
	if m.episodeIndex < 0 {
		return 0, false
	}
	idx := m.episodeIndex + 1
	if idx >= len(m.episodeResults) {
		return 0, false
	}
	return idx, true
}

func (m *modelImpl) canPlayNextEpisode() bool {
	_, ok := m.nextEpisodeIndex()
	return ok
}

func (m *modelImpl) newOpID() int {
	m.nextOpID++
	return m.nextOpID
}

func shorten(text string, maxWidth int) string {
	if text == "" || maxWidth <= 0 {
		return ""
	}
	if lipgloss.Width(text) <= maxWidth {
		return text
	}
	r := []rune(text)
	if len(r) <= 3 {
		return text
	}
	cut := maxWidth - 3
	if cut < 1 {
		cut = 1
	}
	if cut > len(r) {
		cut = len(r)
	}
	return string(r[:cut]) + "..."
}

func sideBySide(left, right string, totalWidth int) string {
	gap := totalWidth - lipgloss.Width(left) - lipgloss.Width(right)
	if gap < 1 {
		gap = 1
	}
	return left + strings.Repeat(" ", gap) + right
}

func twoColumns(left, right string, leftW, rightW int) string {
	leftFixed := lipgloss.NewStyle().Width(leftW).Render(left)
	rightFixed := lipgloss.NewStyle().Width(rightW).Render(right)
	return lipgloss.JoinHorizontal(lipgloss.Top, leftFixed, "  ", rightFixed)
}

func modeBadge(mode string) string {
	return renderBadge(mode)
}
