package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// updateChapters handles keys on the chapter listing.
func (m *modelImpl) updateChapters(msg tea.Msg) (tea.Model, tea.Cmd) {
	if keyMsg, ok := msg.(tea.KeyMsg); ok {
		switch {
		case key.Matches(keyMsg, m.keys.Select):
			m.autoAdvance = 0
			m.fallbackTried = false
			return m.selectChapter(m.selectedChapterIndex())
		case key.Matches(keyMsg, m.keys.Top):
			if m.chapterList.SettingFilter() {
				break
			}
			m.chapterList.Select(0)
			return m, nil
		case key.Matches(keyMsg, m.keys.Bottom):
			if m.chapterList.SettingFilter() {
				break
			}
			if visible := m.chapterList.VisibleItems(); len(visible) > 0 {
				m.chapterList.Select(len(visible) - 1)
			}
			return m, nil
		}
	}
	var cmd tea.Cmd
	m.chapterList, cmd = m.chapterList.Update(msg)
	return m, cmd
}

// readerChromeRows reserves the reader's own header + footer rows above
// the page-image box.
const readerChromeRows = 2

// kittyReaderImageID is the stable Kitty slot for reader pages —
// distinct from the poster slots so cleanup targets exactly it.
const kittyReaderImageID uint32 = 3

// readerBGStyle paints header/footer rows opaque black, matching the
// page box so the whole reader ignores terminal transparency.
var readerBGStyle = lipgloss.NewStyle().Background(lipgloss.Color("#000000")).Foreground(lipgloss.Color("#f2f4f8"))

// readerMutedStyle is the muted variant for reader chrome hints.
var readerMutedStyle = lipgloss.NewStyle().Background(lipgloss.Color("#000000")).Foreground(lipgloss.Color("#8d8d8d"))

// renderReaderFullscreen renders the reader at full terminal size,
// bypassing the app chrome so transparent terminal backgrounds never
// show around the page. Total output is exactly m.height lines.
func (m *modelImpl) renderReaderFullscreen() string {
	width := max(20, m.width)
	boxH := max(5, m.height-readerChromeRows)

	title := ""
	if m.selectedSeries != nil {
		title = m.selectedSeries.Title
	}
	chapter := ""
	if m.selectedChapter != nil {
		chapter = m.selectedChapter.DisplayLabel()
	}
	pageInfo := ""
	if len(m.pages) > 0 {
		pageInfo = fmt.Sprintf("Page %d/%d", m.readerPage+1, len(m.pages))
	}

	header := padBG(readerBGStyle.Render(shorten(title+" · "+chapter, width)), width)
	footerText := fmt.Sprintf("%s · ←/→ pages · n/p chapters · esc back", pageInfo)
	// Flag background work only while the current page itself has
	// nothing to show yet; prefetching ahead must not paint a
	// permanent "loading…" next to a fully rendered page.
	if _, ready := m.readerRender[m.readerPage]; m.loading && !ready {
		footerText += " · loading…"
	}
	footer := padBG(readerMutedStyle.Render(shorten(footerText, width)), width)

	var page string
	// A rendered current page stays on screen even while prefetch loads
	// ahead in the background. The old `&& !m.loading` guard hid it
	// behind "Loading page…", and since the Kitty placement then kept
	// showing the previous page's image, every prefetch read as stuck.
	if rendered, ok := m.readerRender[m.readerPage]; ok {
		page = rendered
	} else if m.readerUnavailable {
		page = readerBGStyle.Render(lipgloss.Place(width, boxH, lipgloss.Center, lipgloss.Center, "Page unavailable"))
	} else {
		page = readerBGStyle.Render(lipgloss.Place(width, boxH, lipgloss.Center, lipgloss.Center, "Loading page…"))
	}

	return strings.Join([]string{header, page, footer}, "\n")
}

// padBG pads a chrome row with black-background spaces to full width.
// lipgloss.Width measures visible cells, so escape-wrapped rows pad
// exactly to the edge.
func padBG(row string, width int) string {
	if gap := width - lipgloss.Width(row); gap > 0 {
		row += readerBGStyle.Render(strings.Repeat(" ", gap))
	}
	return row
}

// updateReader handles page-turning and chapter-stepping keys.
func (m *modelImpl) updateReader(msg tea.Msg) (tea.Model, tea.Cmd) {
	if keyMsg, ok := msg.(tea.KeyMsg); ok {
		switch {
		case key.Matches(keyMsg, m.keys.ReaderNext):
			return m, m.gotoReaderPage(m.readerPage + 1)
		case key.Matches(keyMsg, m.keys.ReaderPrev):
			return m, m.gotoReaderPage(m.readerPage - 1)
		case key.Matches(keyMsg, m.keys.ReaderNextChapter):
			return m.stepReaderChapter(1)
		case key.Matches(keyMsg, m.keys.ReaderPrevChapter):
			return m.stepReaderChapter(-1)
		case key.Matches(keyMsg, m.keys.Top):
			return m, m.gotoReaderPage(0)
		case key.Matches(keyMsg, m.keys.Bottom):
			return m, m.gotoReaderPage(len(m.pages) - 1)
		}
	}
	return m, nil
}
