package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"kari/internal/termimg"
)

func (m *modelImpl) View() string {
	var output string
	if m.showHelp {
		output = m.renderHelpOverlay()
	} else {
		output = m.renderMainView()
	}

	// Kitty images are persistent overlays, not part of the normal text
	// flow — anything that stops showing one (leaving its screen, but also
	// the help overlay or a confirm dialog covering it in place) doesn't
	// remove it on its own, since whatever renders instead never mentions
	// that image again. So this always runs last, against whatever the
	// actual final output is, and deletes a slot's placement whenever this
	// frame doesn't show it (a harmless no-op once nothing is left to
	// delete) — computing it from the header/body building above and
	// hoping every branch remembers to include it is exactly how the help
	// overlay and the preview confirm dialog ended up skipping it before.
	if m.imgProtocol == termimg.ProtocolKitty {
		var cleanup strings.Builder
		if !m.searchPosterVisible() {
			cleanup.WriteString(termimg.DeleteKitty(kittySearchImageID))
		}
		if !m.previewPosterVisible() {
			cleanup.WriteString(termimg.DeleteKitty(kittyPreviewImageID))
		}
		output = cleanup.String() + output
	}

	return output
}

// searchPosterVisible/previewPosterVisible report whether this frame will
// actually show that slot's poster — every state that hides it (switching
// screens, the help overlay, a confirm dialog covering the screen, still
// loading / unresolved media, or disabled images) needs to be listed here
// so the Kitty cleanup above knows to clear it.
func (m *modelImpl) searchPosterVisible() bool {
	return m.activeView == viewSearch && !m.showHelp && m.imagesEnabled && m.searchPoster != ""
}

func (m *modelImpl) previewPosterVisible() bool {
	return m.activeView == viewPreview && !m.showHelp && !m.confirmCompletion && m.resolved != nil && m.imagesEnabled && m.previewPoster != ""
}

func (m *modelImpl) renderMainView() string {
	dims := m.computeLayoutDims()

	header := m.renderHeader(dims)
	rule := m.renderRule(dims.contentW)
	body := m.renderBody(dims)
	footer := m.renderFooter(dims)

	rows := []string{
		header,
		rule,
		"",
		body,
		"",
	}
	if m.loading {
		rows = append(rows, m.renderLoadingLine(dims.contentW))
	}
	if statusLine := m.renderStatusLine(dims.contentW); statusLine != "" {
		rows = append(rows, statusLine)
	}

	content := lipgloss.JoinVertical(lipgloss.Left, rows...)
	contentHeight := lipgloss.Height(content)

	// Calculate how many empty lines are needed to push the footer to the bottom
	gap := m.height - contentHeight - lipgloss.Height(footer)
	if gap < 0 {
		gap = 0
	}

	// Add empty lines as a gap
	spacer := strings.Repeat("\n", gap)
	finalContent := content + spacer + "\n" + footer

	if m.width > dims.contentW {
		leftPad := strings.Repeat(" ", (m.width-dims.contentW)/2)
		lines := strings.Split(finalContent, "\n")
		for i, line := range lines {
			if line != "" {
				lines[i] = leftPad + line
			}
		}
		finalContent = strings.Join(lines, "\n")
	}
	return finalContent
}

func (m *modelImpl) renderRule(width int) string {
	return lipgloss.NewStyle().Foreground(colorBorder).Render(strings.Repeat("─", width))
}

func (m *modelImpl) renderHeader(dims layoutDims) string {
	maxTitleW := max(12, dims.contentW-22)
	breadcrumb := lipgloss.NewStyle().Foreground(colorPrimary).Bold(true).Render("Kari") +
		mutedStyle.Render(" › ") +
		mutedStyle.Render(m.activeViewLabel(maxTitleW))

	return sideBySide(breadcrumb, "", dims.contentW)
}

func (m *modelImpl) renderFooter(dims layoutDims) string {
	bindings := m.shortHelpBindings()
	var bindParts []string
	for _, b := range bindings {
		pair := keyStyle.Render(b.Help().Key) + " " + mutedStyle.Render(b.Help().Desc)
		bindParts = append(bindParts, pair)
	}

	footerContent := strings.Join(bindParts, "  ")
	return lipgloss.NewStyle().Width(dims.contentW).Align(lipgloss.Center).Render(footerContent)
}

func (m *modelImpl) renderLoadingLine(width int) string {
	spinner := infoStyle.Render(m.spinner.View())
	text := sectionTitleStyle.Render(m.loadingText)
	line := spinner + " " + text
	spinnerLine := lipgloss.Place(width, 1, lipgloss.Center, lipgloss.Center, line)

	ratio, ok := m.downloadRatio()
	if !ok {
		return spinnerLine
	}

	barWidth := width - 4
	if barWidth > 60 {
		barWidth = 60
	}
	if barWidth < 10 {
		barWidth = 10
	}
	m.downloadBar.Width = barWidth
	bar := lipgloss.Place(width, 1, lipgloss.Center, lipgloss.Center, m.downloadBar.ViewAs(ratio))

	return lipgloss.JoinVertical(lipgloss.Left, spinnerLine, bar)
}

// downloadRatio reports how far an active download has progressed, for the
// loading line's progress bar — a single download reports its byte
// percentage, a batch download reports completed/total episode count.
// ok is false for every other loading state (search, resolve, etc.), which
// keeps those showing just the spinner and text, unchanged.
func (m *modelImpl) downloadRatio() (float64, bool) {
	if m.downloadOpID == 0 {
		return 0, false
	}
	if m.batchTotal > 0 {
		ratio := (float64(m.batchCurrent) + m.batchEpisodeProgress) / float64(m.batchTotal)
		return ratio, true
	}
	if m.downloadProgress >= 0 {
		return m.downloadProgress / 100, true
	}
	return 0, false
}

func (m *modelImpl) renderStatusLine(width int) string {
	if m.statusText == "" {
		return ""
	}

	var style lipgloss.Style
	switch m.statusType {
	case statusSuccess:
		style = lipgloss.NewStyle().Foreground(colorSuccess)
	case statusWarn:
		style = lipgloss.NewStyle().Foreground(colorWarn)
	case statusError:
		style = lipgloss.NewStyle().Foreground(colorError)
	default:
		style = lipgloss.NewStyle().Foreground(colorInfo)
	}

	// Truncate to avoid breaking layout
	text := m.statusText
	if lipgloss.Width(text) > width-4 {
		text = shorten(text, width-4)
	}

	return lipgloss.Place(width, 1, lipgloss.Center, lipgloss.Center, style.Render(text))
}

func (m *modelImpl) activeViewLabel(maxTitleW int) string {
	switch m.activeView {
	case viewSearch:
		return "Search"
	case viewEpisodes:
		if m.selectedSeries != nil && m.selectedSeries.Title != "" {
			return shorten(m.selectedSeries.Title, maxTitleW) + " › Episodes"
		}
		return "Episodes"
	case viewPreview:
		title := ""
		if m.selectedSeries != nil && m.selectedSeries.Title != "" {
			title = m.selectedSeries.Title
		} else if m.resolved != nil && m.resolved.SeriesTitle != "" {
			title = m.resolved.SeriesTitle
		}
		if title != "" {
			return shorten(title, maxTitleW) + " › Preview"
		}
		return "Preview"
	case viewHistory:
		return "History"
	case viewSettings:
		return "Settings"
	default:
		return "Kari"
	}
}

func (m *modelImpl) renderBody(dims layoutDims) string {
	switch m.activeView {
	case viewSearch:
		return m.renderSearchScreen(dims)
	case viewEpisodes:
		return m.renderEpisodesScreen(dims)
	case viewPreview:
		return m.renderPreviewScreen(dims)
	case viewHistory:
		return m.renderHistoryScreen(dims)
	case viewSettings:
		return m.renderSettingsScreen(dims)
	default:
		return ""
	}
}

func (m *modelImpl) renderHistoryScreen(dims layoutDims) string {
	rows := []string{
		sectionTitleStyle.Render("Watch History"),
		mutedStyle.Render(fmt.Sprintf("%d titles", len(m.historyList.Items()))),
		"",
	}

	if len(m.historyList.Items()) == 0 {
		rows = append(rows, mutedStyle.Render("No watch history yet."))
	} else {
		rows = append(rows, mutedStyle.Render("/ to filter  ·  d delete  ·  D clear all"), "")
		rows = append(rows, m.historyList.View())
	}

	if m.confirmDelete {
		return m.renderConfirmDialog("Delete this title from history?", dims)
	}
	if m.confirmClearHistory {
		return m.renderConfirmDialog("Clear ALL history?", dims)
	}

	return strings.Join(rows, "\n")
}

func (m *modelImpl) renderConfirmDialog(title string, dims layoutDims) string {
	dialogW := min(40, max(24, dims.contentW-4))
	dialogHeight := min(10, max(5, m.height-4))
	dialog := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(colorPrimary).
		Padding(1, 2).
		Width(dialogW).
		Align(lipgloss.Center).
		Render(fmt.Sprintf("%s\n\n[y] Yes    [n] No", title))

	return lipgloss.Place(dims.contentW, dialogHeight, lipgloss.Center, lipgloss.Center, dialog)
}
