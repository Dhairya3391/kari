package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"kari/internal/history"
	"kari/internal/provider"
	"kari/internal/service"
)

// EpisodeItem wraps a provider.Episode with its history/progress metadata for rendering.
type EpisodeItem struct {
	Episode  provider.Episode
	History  *history.Entry
	Selected bool
}

// EpisodesData holds the state needed to render the Episodes screen.
type EpisodesData struct {
	SeriesTitle          string
	Episodes             []provider.Episode
	filteredEpisodes     []provider.Episode
	episodeIndices       []int
	episodeListReady     bool
	HistoryIndex         history.EpisodeIndex
	SelectedIndex        int
	SeasonCount          int
	ActiveSeason         int
	SelectMode           bool
	SelectedIDs          map[int]struct{}
	FilterQuery          string
	Filtering            bool // true while the user is typing a / filter
	Loading              bool
	LoadingText          string
	SpinnerFrame         string
	Width                int
	Height               int
	Accent               lipgloss.AdaptiveColor
	PosterBlock          string
	SeriesYear           string
	SeriesKind           string
	SeriesRating         string
	SeriesGenres         []string
	CardHeight           int
	TermWidth            int
	SelectedEpisodeTitle string
}

// RenderEpisodesScreen renders the Episodes screen matching KARI_TUI_SPEC §4.3 and plan §5.3.
func RenderEpisodesScreen(data EpisodesData) string {
	st := NewStyles(data.Accent)

	showPane := data.TermWidth >= 80 && data.Width >= 65
	listW := data.Width
	paneW := 0
	if showPane {
		paneW = min(32, max(22, data.Width/4))
		listW = data.Width - paneW - 4
		if listW < 36 {
			listW = 36
			paneW = data.Width - listW - 4
		}
	}

	leftLines := renderEpisodeList(data, listW, st)
	if !showPane {
		return strings.Join(leftLines, "\n")
	}
	cardHeight := data.CardHeight
	if cardHeight <= 0 {
		cardHeight = data.Height
	}
	rightLines := renderInspectorPane(data, paneW, cardHeight, st)
	lines := max(len(leftLines), len(rightLines))
	return joinColumns(padLines(leftLines, lines), padLines(rightLines, lines), listW, paneW)
}

// renderInspectorPane shows the series identity, score, genres, and selected
// episode beside the list. The synopsis lives only on the preview page.
func renderInspectorPane(data EpisodesData, paneW, height int, st Styles) []string {
	posterRows := detailsPosterRows(height)
	lineCount := posterRows + 7
	lines := strings.Split(fixedBlock(data.PosterBlock, paneW, posterRows), "\n")
	lines = append(lines, "", "")

	title := data.SeriesTitle
	if title == "" {
		title = "—"
	}
	lines = append(lines, truncate(title, paneW))

	var metaParts []string
	if data.SeriesYear != "" {
		metaParts = append(metaParts, st.Dim.Render(data.SeriesYear))
	}
	if data.SeriesKind != "" {
		metaParts = append(metaParts, st.Dim.Render(data.SeriesKind))
	}
	if data.SeriesRating != "" {
		metaParts = append(metaParts, st.Current.Render("★ ")+st.Dim.Render(data.SeriesRating))
	}
	if len(metaParts) == 0 {
		lines = append(lines, "")
	} else {
		lines = append(lines, truncate(strings.Join(metaParts, st.Dim.Render(" · ")), paneW))
	}

	if len(data.SeriesGenres) == 0 {
		lines = append(lines, "")
	} else {
		lines = append(lines, st.Dim.Render(truncate(strings.Join(data.SeriesGenres, " · "), paneW)))
	}

	lines = append(lines, "")
	if data.SelectedEpisodeTitle == "" {
		lines = append(lines, "")
	} else {
		lines = append(lines, st.Dim.Render("selected  ")+truncate(data.SelectedEpisodeTitle, max(1, paneW-10)))
	}

	return padLines(lines, lineCount)
}

// joinColumns merges left and right line slices side-by-side with a fixed gutter.
func joinColumns(left, right []string, leftW, _ int) string {
	n := max(len(left), len(right))
	out := make([]string, n)
	for i := range out {
		l, r := "", ""
		if i < len(left) {
			l = left[i]
		}
		if i < len(right) {
			r = right[i]
		}
		lPad := leftW - lipgloss.Width(l)
		if lPad < 0 {
			lPad = 0
		}
		out[i] = l + strings.Repeat(" ", lPad) + strings.Repeat(" ", posterGutter) + r
	}
	return strings.Join(out, "\n")
}

// renderEpisodeList renders the left episode list column and returns a slice of lines.
func renderEpisodeList(data EpisodesData, listW int, st Styles) []string {
	var rows []string

	totalText := fmt.Sprintf("%d episodes", len(data.Episodes))
	if len(data.Episodes) == 1 {
		totalText = "1 episode"
	}
	headerRight := st.Dim.Render(totalText)
	headerLeft := st.Bold.Render(truncate(data.SeriesTitle, max(10, listW-lipgloss.Width(headerRight)-1)))
	gap := max(1, listW-lipgloss.Width(headerLeft)-lipgloss.Width(headerRight))
	rows = append(rows, headerLeft+strings.Repeat(" ", gap)+headerRight)

	if data.SeasonCount > 1 {
		seasonStrip := RenderSeasonTabs(data.SeasonCount, data.ActiveSeason, data.Accent, listW)
		rows = append(rows, "", seasonStrip)
	}

	rows = append(rows, "")

	if data.Loading {
		return rows
	}

	if len(data.Episodes) == 0 {
		rows = append(rows, st.Dim.Render("No episodes found for this season."))
		return rows
	}

	seasonEpisodes, origIndices := data.filteredEpisodes, data.episodeIndices
	if !data.episodeListReady {
		seasonEpisodes, origIndices = service.FilterEpisodeList(data.Episodes, data.ActiveSeason, data.FilterQuery)
	}

	if data.Filtering || strings.TrimSpace(data.FilterQuery) != "" {
		rows = append(rows, st.Dim.Render("/")+data.FilterQuery+st.Cursor.Render("▌"))
	}

	if len(seasonEpisodes) == 0 {
		rows = append(rows, st.Dim.Render("No episodes match. (esc clears the filter)"))
		return rows
	}

	// 3. Episode rows with scroll window and hints. windowRange reserves the
	// two hint rows so the viewport never shifts when a hint appears.
	startIndex, endIndex, above, below := windowRange(len(seasonEpisodes), data.SelectedIndex, data.Height, len(rows))
	if hint := windowHint(above, true, st); hint != "" {
		rows = append(rows, hint)
	}

	multiSeason := data.SeasonCount > 1
	rightCols := make([]string, endIndex-startIndex)
	rightW := 0
	for i := startIndex; i < endIndex; i++ {
		rightCols[i-startIndex] = episodeRightCol(seasonEpisodes[i], historyEntryFor(data, seasonEpisodes[i]), i == data.SelectedIndex, data.Accent, st)
		if w := lipgloss.Width(rightCols[i-startIndex]); w > rightW {
			rightW = w
		}
	}

	for i := startIndex; i < endIndex; i++ {
		ep := seasonEpisodes[i]
		isFocused := i == data.SelectedIndex
		rows = append(rows, renderEpisodeRow(ep, origIndices[i], isFocused, multiSeason, rightCols[i-startIndex], rightW, data, listW, st))
	}
	if hint := windowHint(below, false, st); hint != "" {
		rows = append(rows, hint)
	}

	// In Select Mode, show selection count summary line before footer
	if data.SelectMode {
		selCount := len(data.SelectedIDs)
		summary := fmt.Sprintf("%d selected", selCount)
		rows = append(rows, "", st.Dim.Render(summary))
	}

	return rows
}

// FilterEpisodesByText narrows episodes by title or number, keeping the
// original indices aligned. Empty query returns the input unchanged. The
// model and the renderer share it so cursor positions always agree.
func FilterEpisodesByText(episodes []provider.Episode, idxs []int, query string) ([]provider.Episode, []int) {
	return service.FilterEpisodesByText(episodes, idxs, query)
}

// filterToSeason returns the episodes for the active season and their original indices.
func filterToSeason(episodes []provider.Episode, _ int, activeSeason int) ([]provider.Episode, []int) {
	return service.FilterEpisodeList(episodes, activeSeason, "")
}

// historyEntryFor looks up progress for one episode, or nil.
func historyEntryFor(data EpisodesData, ep provider.Episode) *history.Entry {
	if e, ok := data.HistoryIndex.Get(ep.Season, ep.Episode); ok {
		return &e
	}
	return nil
}

// episodeCode renders the row code: S01E05 for multi-season shows, 01
// otherwise. index is the original position, used when the provider
// reports episode 0.
func episodeCode(ep provider.Episode, index int, multiSeason bool) string {
	num := ep.Episode
	if num == 0 {
		num = index + 1
	}
	if multiSeason && ep.Season > 0 {
		return fmt.Sprintf("S%02dE%02d", ep.Season, num)
	}
	return fmt.Sprintf("%02d", num)
}

// episodeDisplayTitle returns the provider title, or "Episode N" when the
// provider sends none.
func episodeDisplayTitle(ep provider.Episode, index int) string {
	if ep.Title != "" {
		return ep.Title
	}
	num := ep.Episode
	if num == 0 {
		num = index + 1
	}
	return fmt.Sprintf("Episode %d", num)
}

// episodeRightCol renders the trailing status: progress bar plus time
// left for in-progress episodes, runtime when known, else empty.
func episodeRightCol(_ provider.Episode, historyEntry *history.Entry, isFocused bool, accent lipgloss.AdaptiveColor, st Styles) string {
	if historyEntry != nil {
		if historyEntry.Complete || historyEntry.PercentComplete >= 0.95 || (historyEntry.DurationSecs > 0 && (historyEntry.PositionSecs/historyEntry.DurationSecs) >= 0.95) {
			return ""
		}
		if historyEntry.PositionSecs > 0 && historyEntry.DurationSecs > 0 {
			ratio := historyEntry.PositionSecs / historyEntry.DurationSecs
			progressBar := RenderProgressBar(ratio, 8, accent)
			leftSecs := historyEntry.DurationSecs - historyEntry.PositionSecs
			leftStr := fmt.Sprintf("%dm left", int(leftSecs/60))
			if isFocused {
				return progressBar + "  " + st.Current.Render(leftStr)
			}
			return progressBar + "  " + st.Dim.Render(leftStr)
		}
		if historyEntry.DurationSecs > 0 {
			durStr := formatDurationShort(historyEntry.DurationSecs)
			if isFocused {
				return st.Current.Render(durStr)
			}
			return st.Dim.Render(durStr)
		}
	}
	return ""
}

func renderEpisodeRow(ep provider.Episode, index int, isFocused bool, multiSeason bool, rightCol string, rightW int, data EpisodesData, listW int, st Styles) string {
	code := episodeCode(ep, index, multiSeason)
	title := episodeDisplayTitle(ep, index)
	historyEntry := historyEntryFor(data, ep)

	if data.SelectMode {
		cursor := "  "
		if isFocused {
			cursor = st.Cursor.Render("▌ ")
		}

		_, isSelected := data.SelectedIDs[index]
		box := "[ ] "
		if isSelected {
			box = "[x] "
		}

		titleTrunc := truncate(title, listW-18)
		if isFocused {
			titleTrunc = st.Current.Render(titleTrunc)
		}

		codeStr := st.Dim.Render(code)
		if isFocused {
			codeStr = st.Current.Render(code)
		}

		return fmt.Sprintf("%s%s%s  %s", cursor, st.Dim.Render(box), codeStr, titleTrunc)
	}

	// Normal Mode
	cursor := "  "
	if isFocused {
		cursor = st.Cursor.Render("▌ ")
	}

	statusMarker := "  "
	if historyEntry != nil && (historyEntry.Complete || historyEntry.PercentComplete >= 0.95 || (historyEntry.DurationSecs > 0 && (historyEntry.PositionSecs/historyEntry.DurationSecs) >= 0.95)) {
		statusMarker = st.Ok.Render("✓ ")
	}

	// Pad the right column to the widest visible one so titles align.
	rightPadded := rightCol + strings.Repeat(" ", max(0, rightW-lipgloss.Width(rightCol)))

	fixedW := 2 + 2 + lipgloss.Width(code) + 2 + 2 + rightW
	titleW := listW - fixedW
	if titleW < 15 {
		titleW = 15
	}

	titleTrunc := truncate(title, titleW)
	titlePadded := titleTrunc + strings.Repeat(" ", max(0, titleW-lipgloss.Width(titleTrunc)))
	if isFocused {
		titlePadded = st.Current.Render(titleTrunc) + strings.Repeat(" ", max(0, titleW-lipgloss.Width(titleTrunc)))
	}

	codeStr := st.Dim.Render(code)
	if isFocused {
		codeStr = st.Current.Render(code)
	}

	return fmt.Sprintf("%s%s%s  %s  %s", cursor, statusMarker, codeStr, titlePadded, rightPadded)
}
