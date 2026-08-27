package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"kari/internal/termimg"
)

// searchLeftWidth is the width of the search screen's left (results) column
// — shared with resizeLists so the list's own delegate truncates titles to
// the width it's actually rendered at, instead of the full content width.
// Without this, a long title fits the list's assumed width, skips
// truncation, and gets hard-wrapped instead by the Width() applied when
// this column is boxed in later, breaking the list's row alignment.
func searchLeftWidth(contentW int) int {
	if contentW <= narrowTerminalThreshold {
		return contentW
	}
	return contentW * 65 / 100
}

func (m *modelImpl) renderSearchScreen(dims layoutDims) string {
	if dims.contentW <= narrowTerminalThreshold {
		return m.renderSearchLeft(dims.contentW)
	}

	leftW := searchLeftWidth(dims.contentW)
	rightW := dims.contentW - leftW - 2
	leftCol := lipgloss.NewStyle().Width(leftW).Render(m.renderSearchLeft(leftW))
	rightCol := lipgloss.NewStyle().Width(rightW).Render(m.renderSearchRight())

	// The poster is joined on below the (already width-fixed) text column
	// rather than included inside renderSearchRight before wrapping: lipgloss
	// word-wraps content that doesn't fit a Style's Width, and since each
	// image row is one long space-free escape sequence, a wrap mid-row would
	// slice straight through it and corrupt the rest of the frame.
	// JoinVertical/JoinHorizontal only pad blocks to align them — they never
	// wrap — so this is safe regardless of terminal width.
	if block := posterBlock(m.searchPoster, m.searchPosterUnavailable, m.imgProtocol, kittySearchImageID, m.imagesEnabled); block != "" {
		rightCol = lipgloss.JoinVertical(lipgloss.Left, rightCol, "", block)
	}

	return lipgloss.JoinHorizontal(lipgloss.Top, leftCol, "  ", rightCol)
}

// posterBlock returns rendered (the image) if present, a muted "no image
// available" line once a fetch has definitively failed or found no artwork,
// a muted "image rendering disabled" line when the user has turned image
// rendering off in settings, or "" while still loading — callers should
// omit the block entirely in that last case rather than reserving space for
// it, to avoid a layout jump when the fetch resolves.
func posterBlock(rendered string, unavailable bool, protocol termimg.Protocol, imageID uint32, imagesEnabled bool) string {
	if imagesEnabled && rendered != "" {
		return rendered
	}

	// Whenever this slot has no image to show — cleared results, a mode
	// switch, a fetch that hasn't finished yet, one that failed, or images
	// being turned off — any previous Kitty placement in it needs to be
	// explicitly deleted too. Unlike text, it doesn't just get overwritten
	// by whatever renders in its place next; nothing here even mentions
	// that image again unless we say so, so without this it keeps showing
	// the last thing it had.
	var cleanup string
	if protocol == termimg.ProtocolKitty {
		cleanup = termimg.DeleteKitty(imageID)
	}

	if !imagesEnabled {
		return cleanup + mutedStyle.Render("Image rendering disabled")
	}
	if unavailable {
		return cleanup + mutedStyle.Render("No image available")
	}
	return cleanup
}

func (m *modelImpl) renderSearchLeft(width int) string {
	rows := []string{
		sectionTitleStyle.Render("Search"),
		"",
		m.queryInput.View(),
		"",
	}

	if len(m.seriesResults) == 0 {
		if m.historyStore != nil {
			all := m.historyStore.All()
			if len(all) > 0 {
				last := all[0]
				lastPlayed := fmt.Sprintf("Last: %s · %s", last.Title, relativeTime(last.WatchedAt))
				rows = append(rows, mutedStyle.Render(lastPlayed)+"  "+mutedStyle.Render("[H] history"), "")
			}
		}
		rows = append(rows, mutedStyle.Render("No results yet — type a query and press Enter"))
	} else {
		count := fmt.Sprintf("%d results", len(m.seriesResults))
		rows = append(rows, mutedStyle.Render(count)+"  "+mutedStyle.Render("/ to filter"), "")
		rows = append(rows, m.seriesList.View())
	}

	return strings.Join(rows, "\n")
}

func (m *modelImpl) renderSearchRight() string {
	var rows []string

	rows = append(rows, sectionTitleStyle.Render("Modes"), "")
	for _, mode := range m.modes {
		if mode == m.appMode {
			rows = append(rows, lipgloss.NewStyle().Foreground(colorPrimary).Render("● "+strings.ToUpper(string(mode))))
		} else {
			rows = append(rows, mutedStyle.Render("○ "+strings.ToUpper(string(mode))))
		}
	}

	rows = append(rows, "", sectionTitleStyle.Render("Players"), "")
	for _, p := range m.availablePlayers {
		if p == m.selectedPlayerName() {
			rows = append(rows, lipgloss.NewStyle().Foreground(colorPrimary).Render("● "+strings.ToUpper(p)))
		} else {
			rows = append(rows, mutedStyle.Render("○ "+strings.ToUpper(p)))
		}
	}

	return lipgloss.NewStyle().PaddingLeft(2).Render(strings.Join(rows, "\n"))
}

func (m *modelImpl) renderEpisodesScreen(dims layoutDims) string {
	seriesTitle := "Episodes"
	if m.selectedSeries != nil {
		seriesTitle = m.selectedSeries.Title
	}

	selCount := len(m.selectedEpisodes)
	selInfo := ""
	if selCount > 0 {
		selInfo = lipgloss.NewStyle().Foreground(colorPrimary).Render(fmt.Sprintf(" · %d selected — [D]ownload", selCount))
	}

	episodeCount := mutedStyle.Render(fmt.Sprintf("%d episodes", len(m.episodeResults)))
	if m.modeFeatures().AudioSelection && m.audioMode != "" {
		episodeCount += "  " + badgeBase.Background(colorInfo).Render(strings.ToUpper(m.audioMode))
	}

	rows := []string{
		mutedStyle.Render("← ") + sectionTitleStyle.Render(shorten(seriesTitle, dims.contentW-12)) + selInfo,
		episodeCount,
		"",
	}

	if len(m.episodeResults) == 0 {
		rows = append(rows, mutedStyle.Render("No episodes available."))
	} else {
		rows = append(rows, mutedStyle.Render("space toggle · ctrl+a all · ctrl+d none  ·  / to filter  ·  g/G top/bottom"), "")
		rows = append(rows, m.episodeList.View())
	}

	return strings.Join(rows, "\n")
}
