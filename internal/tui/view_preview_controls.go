package tui

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"kari/internal/model"
	"kari/internal/service"
)

func (m *modelImpl) filteredPlayback() []int {
	if m.resolved == nil {
		return nil
	}
	if len(m.resolved.Playback) == 0 {
		return nil
	}
	return service.FilterPlaybackIndices(m.resolved.Playback, m.qualityMode, m.languageFilter)
}

// renderPreviewControlsRow lays Source, Players (+Autoplay), and Actions out
// as three side-by-side columns spanning the full width instead of one tall
// column pinned to the right with the rest of the screen sitting empty
// beside it, falling back to a stacked single column on narrow terminals.
func (m *modelImpl) renderPreviewControlsRow(width int) string {
	filtered := m.filteredPlayback()
	if len(filtered) == 0 {
		return mutedStyle.Render("No sources available")
	}

	sourceW := width * 44 / 100
	playersW := width * 26 / 100
	actionsW := width - sourceW - playersW - 4
	if width < narrowTerminalThreshold {
		sourceW, playersW, actionsW = width, width, width
	}

	source := m.renderSourceColumn(filtered, sourceW)
	players := m.renderPlayersColumn()
	actions := m.renderActionsColumn()

	if width < narrowTerminalThreshold {
		return lipgloss.JoinVertical(lipgloss.Left, source, "", players, "", actions)
	}

	cols := []string{
		lipgloss.NewStyle().Width(sourceW).Render(source),
		lipgloss.NewStyle().Width(playersW).Render(players),
		lipgloss.NewStyle().Width(actionsW).Render(actions),
	}
	return lipgloss.JoinHorizontal(lipgloss.Top, cols[0], "  ", cols[1], "  ", cols[2])
}

const maxVisibleSources = 5

var (
	reResolutionTier = regexp.MustCompile(`(?i)\b(2160p?|4k|uhd|1440p?|2k|qhd|1080p?|fhd|720p?|hd|480p?|360p?|576p?|sd)\b`)
	reSourceBracket  = regexp.MustCompile(`\[([^\]]+)\]`)
)

func parseQualityAndSource(rawQuality, defaultResolver string) (qualityDisplay, sourceDisplay string) {
	q := strings.TrimSpace(rawQuality)
	if q == "" {
		return "Unknown", defaultResolver
	}

	tier := "FHD"
	if m := reResolutionTier.FindString(q); m != "" {
		tier = formatResolutionTier(m)
	}

	extra := ""
	if idx := strings.Index(q, "("); idx != -1 {
		extra = strings.TrimSpace(q[idx:])
	}

	qualityDisplay = tier
	if extra != "" {
		qualityDisplay = tier + " " + extra
	}

	if m := reSourceBracket.FindStringSubmatch(q); len(m) == 2 {
		sourceDisplay = strings.TrimSpace(m[1])
	} else {
		sourceDisplay = defaultResolver
	}

	return qualityDisplay, sourceDisplay
}

func formatResolutionTier(res string) string {
	lower := strings.ToLower(strings.TrimSpace(res))
	switch {
	case strings.Contains(lower, "4k") || strings.Contains(lower, "uhd") || strings.HasPrefix(lower, "2160"):
		return "4K"
	case strings.Contains(lower, "qhd") || strings.Contains(lower, "2k") || strings.HasPrefix(lower, "1440"):
		return "QHD"
	case strings.Contains(lower, "fhd") || strings.HasPrefix(lower, "1080"):
		return "FHD"
	case strings.EqualFold(lower, "hd") || strings.HasPrefix(lower, "720"):
		return "HD"
	case strings.EqualFold(lower, "sd") || strings.HasPrefix(lower, "480") || strings.HasPrefix(lower, "360") || strings.HasPrefix(lower, "576"):
		return "SD"
	default:
		return strings.ToUpper(res)
	}
}

func (m *modelImpl) renderSourceColumn(filtered []int, width int) string {
	r := m.resolved
	type sourceRow struct {
		actualIdx int
		quality   string
		provider  string
	}

	selectedPos := 0
	for i, actualIdx := range filtered {
		if actualIdx == m.selectedPlayback {
			selectedPos = i
			break
		}
	}

	rowsData := make([]sourceRow, 0, len(filtered))
	maxQualityW := 0
	for _, actualIdx := range filtered {
		src := r.Playback[actualIdx]
		q, p := parseQualityAndSource(src.Quality, m.registry.DisplayName(src.Resolver))
		if w := lipgloss.Width(q); w > maxQualityW {
			maxQualityW = w
		}
		rowsData = append(rowsData, sourceRow{
			actualIdx: actualIdx,
			quality:   q,
			provider:  p,
		})
	}

	items := make([]string, 0, len(rowsData))
	for _, row := range rowsData {
		gap := maxQualityW - lipgloss.Width(row.quality)
		if gap < 0 {
			gap = 0
		}
		paddedQuality := row.quality + strings.Repeat(" ", gap)

		var line string
		if row.provider != "" {
			if row.actualIdx == m.selectedPlayback {
				line = lipgloss.NewStyle().Foreground(colorPrimary).Bold(true).Render("● "+paddedQuality) +
					mutedStyle.Render("  · "+row.provider)
			} else {
				line = mutedStyle.Render("○ " + paddedQuality + "  · " + row.provider)
			}
		} else {
			if row.actualIdx == m.selectedPlayback {
				line = lipgloss.NewStyle().Foreground(colorPrimary).Bold(true).Render("● " + paddedQuality)
			} else {
				line = mutedStyle.Render("○ " + paddedQuality)
			}
		}
		items = append(items, line)
	}

	// Window the items to maxVisibleSources to keep the preview UI compact and stable
	visibleItems := items
	if len(items) > maxVisibleSources {
		start := selectedPos - maxVisibleSources/2
		if start < 0 {
			start = 0
		}
		end := start + maxVisibleSources
		if end > len(items) {
			end = len(items)
			start = end - maxVisibleSources
			if start < 0 {
				start = 0
			}
		}
		visibleItems = items[start:end]
	}

	sourceTitle := "Source"
	if len(filtered) > 1 {
		sourceTitle = fmt.Sprintf("Source (%d/%d)", selectedPos+1, len(filtered))
	}
	rows := []string{
		sectionTitleStyle.Render(sourceTitle),
		"",
		strings.Join(visibleItems, "\n"),
		"",
		mutedStyle.Render("tab / shift+tab to switch"),
	}
	return strings.Join(rows, "\n")
}

func (m *modelImpl) renderPlayersColumn() string {
	r := m.resolved
	rows := []string{sectionTitleStyle.Render("Players"), ""}
	for _, p := range m.availablePlayers {
		if p == m.selectedPlayerName() {
			rows = append(rows, lipgloss.NewStyle().Foreground(colorPrimary).Render("● "+strings.ToUpper(p)))
		} else {
			rows = append(rows, mutedStyle.Render("○ "+strings.ToUpper(p)))
		}
	}
	rows = append(rows, "", mutedStyle.Render("[ctrl+p] to switch player"))

	if model.IsEpisodeBased(r.MediaType) {
		status := mutedStyle.Render("OFF")
		if m.autoplay {
			status = lipgloss.NewStyle().Foreground(colorSuccess).Render("ON")
		}
		rows = append(rows, "", sectionTitleStyle.Render("Autoplay"), "")
		rows = append(rows, lipgloss.NewStyle().Foreground(colorPrimary).Render("[A]")+"  "+textStyle.Render("Status: ")+status)
	}
	return strings.Join(rows, "\n")
}

func (m *modelImpl) renderActionsColumn() string {
	r := m.resolved
	rows := []string{sectionTitleStyle.Render("Actions"), ""}
	rows = append(rows, keyStyle.Render("enter")+"  "+textStyle.Render("Play"))
	if r.StartTime > 5 {
		rows = append(rows, keyStyle.Render("r")+"      "+textStyle.Render("Restart"))
	}
	if m.canPlayNextEpisode() {
		rows = append(rows, keyStyle.Render("n")+"      "+textStyle.Render("Play next"))
	}
	rows = append(rows, keyStyle.Render("d")+"      "+textStyle.Render("Download"))
	return strings.Join(rows, "\n")
}

func fillerBadge() string {
	return fillerBadgeStr
}

func (m *modelImpl) renderHelpOverlay() string {
	navLines := []string{
		sectionTitleStyle.Render("Navigation & Search"),
		"",
		"  " + keyStyle.Render("↑/↓ j/k") + "   " + mutedStyle.Render("move selection"),
		"  " + keyStyle.Render("g / G") + "     " + mutedStyle.Render("top / bottom"),
		"  " + keyStyle.Render("esc") + "       " + mutedStyle.Render("back / cancel"),
		"  " + keyStyle.Render("space") + "     " + mutedStyle.Render("focus search box"),
		"  " + keyStyle.Render("enter") + "     " + mutedStyle.Render("search / select"),
		"  " + keyStyle.Render("tab") + "       " + mutedStyle.Render("switch content mode"),
		"  " + keyStyle.Render("/") + "         " + mutedStyle.Render("filter results"),
		"",
		sectionTitleStyle.Render("Views & System"),
		"",
		"  " + keyStyle.Render("h") + "         " + mutedStyle.Render("watch history"),
		"  " + keyStyle.Render("s") + "         " + mutedStyle.Render("settings"),
		"  " + keyStyle.Render("ctrl+p") + "    " + mutedStyle.Render("switch player"),
		"  " + keyStyle.Render("?") + "         " + mutedStyle.Render("toggle this help"),
		"  " + keyStyle.Render("q") + "         " + mutedStyle.Render("quit kari"),
	}

	epKeys := []string{
		sectionTitleStyle.Render("Episodes & Seasons"),
		"",
		"  " + keyStyle.Render("space") + "     " + mutedStyle.Render("toggle select"),
		"  " + keyStyle.Render("[ / ]") + "     " + mutedStyle.Render("jump season"),
		"  " + keyStyle.Render("1 - 9") + "     " + mutedStyle.Render("season 1 - 9"),
		"  " + keyStyle.Render("ctrl+a") + "    " + mutedStyle.Render("select all"),
		"  " + keyStyle.Render("ctrl+d") + "    " + mutedStyle.Render("deselect all"),
		"  " + keyStyle.Render("D") + "         " + mutedStyle.Render("batch download"),
	}
	if m.modeFeatures().AudioSelection {
		epKeys = append(epKeys, "  "+keyStyle.Render("a")+"         "+mutedStyle.Render("sub / dub audio"))
	}

	playbackLines := []string{
		"",
		sectionTitleStyle.Render("Playback & Actions"),
		"",
		"  " + keyStyle.Render("enter/p") + "   " + mutedStyle.Render("play stream"),
		"  " + keyStyle.Render("n") + "         " + mutedStyle.Render("play next episode"),
		"  " + keyStyle.Render("r") + "         " + mutedStyle.Render("restart from start"),
		"  " + keyStyle.Render("A") + "         " + mutedStyle.Render("toggle autoplay"),
		"  " + keyStyle.Render("d") + "         " + mutedStyle.Render("download stream"),
		"  " + keyStyle.Render("tab") + "       " + mutedStyle.Render("switch source"),
		"  " + keyStyle.Render("x") + "         " + mutedStyle.Render("cancel download"),
	}
	rightLines := append(epKeys, playbackLines...)

	titleRow := lipgloss.NewStyle().Bold(true).Foreground(colorPrimary).Render("⌨  KEYBOARD SHORTCUTS")
	footerHint := mutedStyle.Render("[?] or [Esc] close cheatsheet")

	var body string
	var boxW int
	if m.width >= 76 {
		colW := 33
		leftCol := lipgloss.NewStyle().Width(colW).Render(strings.Join(navLines, "\n"))
		rightCol := lipgloss.NewStyle().Width(colW).Render(strings.Join(rightLines, "\n"))
		body = lipgloss.JoinHorizontal(lipgloss.Top, leftCol, "    ", rightCol)
		boxW = colW*2 + 8
	} else {
		allLines := append(navLines, append([]string{""}, rightLines...)...)
		body = strings.Join(allLines, "\n")
		boxW = min(50, max(24, m.width-6))
	}

	content := lipgloss.JoinVertical(lipgloss.Left, titleRow, "", body, "", footerHint)
	innerH := max(1, m.height-6)
	content, m.helpScroll = scrollLines(content, m.helpScroll, innerH, "↑/↓ scroll")

	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(colorPrimary).
		Padding(1, 2).
		Width(boxW).
		Render(content)

	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, box, lipgloss.WithWhitespaceChars(" "), lipgloss.WithWhitespaceForeground(lipgloss.Color("0")))
}
