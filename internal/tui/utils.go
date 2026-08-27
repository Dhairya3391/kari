package tui

import (
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

const (
	maxContentWidth         = 118
	narrowTerminalThreshold = 90
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
	w := max(20, dims.bodyW-4)
	h := max(4, m.height-12)

	seriesW := max(20, searchLeftWidth(dims.bodyW)-4)
	m.seriesList.SetSize(seriesW, h)
	m.episodeList.SetSize(w, h)
	m.historyList.SetSize(w, h)

	inputW := max(15, dims.contentW-12)
	m.queryInput.Width = inputW
	m.authInput.Width = inputW
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

// statusDuration* give every auto-clearing status message a consistent
// lifetime by severity, instead of the ad hoc 3s/5s/7s/8s literals that
// used to be picked independently at each call site — errors/warnings
// linger longer since they're more likely to need re-reading.
const (
	statusDurationInfo    = 3 * time.Second
	statusDurationSuccess = 4 * time.Second
	statusDurationWarn    = 5 * time.Second
	statusDurationError   = 6 * time.Second
)

func statusClearDuration(level statusLevel) time.Duration {
	switch level {
	case statusError:
		return statusDurationError
	case statusWarn:
		return statusDurationWarn
	case statusSuccess:
		return statusDurationSuccess
	default:
		return statusDurationInfo
	}
}

// setStatusTimed sets the status line and returns a Cmd that clears it
// after statusClearDuration(level), so callers that want an auto-clearing
// status don't each pick their own duration.
func (m *modelImpl) setStatusTimed(level statusLevel, text string) tea.Cmd {
	m.setStatus(level, text)
	return m.clearStatusAfter(statusClearDuration(level))
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

func modeBadge(mode string) string {
	return renderBadge(mode)
}

func wrapWithEllipsis(text string, width, maxLines int) string {
	lines := wrapText(text, width)
	if len(lines) <= maxLines {
		return strings.Join(lines, "\n")
	}

	lines = lines[:maxLines]
	last := lines[maxLines-1]
	for lipgloss.Width(last)+3 > width && last != "" {
		last = strings.TrimRight(last[:len(last)-1], " ")
	}
	lines[maxLines-1] = last + "..."
	return strings.Join(lines, "\n")
}

func wrapText(text string, width int) []string {
	words := strings.Fields(text)
	if len(words) == 0 {
		return nil
	}

	lines := make([]string, 0, len(words)/8+1)
	cur := words[0]
	for _, word := range words[1:] {
		candidate := cur + " " + word
		if lipgloss.Width(candidate) > width {
			lines = append(lines, cur)
			cur = word
			continue
		}
		cur = candidate
	}
	return append(lines, cur)
}

// wrapEntries flows already-rendered entries (may contain ANSI styling)
// into lines no wider than width, joining same-line entries with gap —
// the same word-wrap shape as wrapText, but for a list of discrete chips
// instead of a paragraph of words.
func wrapEntries(entries []string, width int, gap string) []string {
	if len(entries) == 0 {
		return nil
	}
	gapWidth := lipgloss.Width(gap)

	var lines []string
	cur := entries[0]
	curWidth := lipgloss.Width(cur)
	for _, e := range entries[1:] {
		w := lipgloss.Width(e)
		if curWidth+gapWidth+w > width {
			lines = append(lines, cur)
			cur = e
			curWidth = w
			continue
		}
		cur += gap + e
		curWidth += gapWidth + w
	}
	return append(lines, cur)
}
