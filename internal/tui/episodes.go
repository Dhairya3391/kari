package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"kari/internal/history"
	"kari/internal/provider"
)

// EpisodeItem wraps a provider.Episode with its history/progress metadata for rendering.
type EpisodeItem struct {
	Episode  provider.Episode
	History  *history.Entry
	Selected bool
}

// EpisodesData holds the state needed to render the Episodes screen.
type EpisodesData struct {
	SeriesTitle   string
	Episodes      []provider.Episode
	HistoryIndex  history.EpisodeIndex
	SelectedIndex int
	SeasonCount   int
	ActiveSeason  int
	SelectMode    bool
	SelectedIDs   map[int]struct{}
	FilterQuery   string
	Filtering     bool // true while the user is typing a / filter
	Loading       bool
	LoadingText   string
	SpinnerFrame  string
	Width         int
	Height        int
	Accent        lipgloss.AdaptiveColor
	// Inspector pane fields (shown at >= 110 terminal width)
	PosterBlock    string // pre-rendered poster or half-block art
	SeriesYear     string // "2023"
	SeriesKind     string // "TV", "Anime", etc.
	SeriesRating   string // "9.38" — omit if empty
	SeriesGenres   []string
	SeriesOverview string
	TermWidth      int // full terminal width (not content width)
	// Selected episode detail for pane bottom
	SelectedEpisodeTitle string
	SelectedEpisodeAired string // "2023-09-29" — omit if empty
}

// RenderEpisodesScreen renders the Episodes screen matching KARI_TUI_SPEC §4.3 and plan §5.3.
func RenderEpisodesScreen(data EpisodesData) string {
	st := NewStyles(data.Accent)

	// Determine whether to show the right inspector pane.
	showPane := data.TermWidth >= 95 && data.Width >= 65

	// Compute column widths.
	listW := data.Width
	paneW := 0
	if showPane {
		paneW = min(36, max(24, data.Width/3))
		listW = data.Width - paneW - 4 // 4-char generous gutter
		if listW < 36 {
			listW = 36
			paneW = data.Width - listW - 4
		}
	}

	leftLines := renderEpisodeList(data, listW, st)
	if !showPane {
		return strings.Join(leftLines, "\n")
	}

	rightLines := renderInspectorPane(data, paneW, st)
	return joinColumns(leftLines, rightLines, listW, paneW)
}

// renderEpisodeList renders the left episode list column and returns a slice of lines.
func renderEpisodeList(data EpisodesData, listW int, st Styles) []string {
	var rows []string

	// 1. Series title header, truncated so long titles never push
	// past the column into the inspector pane.
	totalText := fmt.Sprintf("%d episodes", len(data.Episodes))
	if len(data.Episodes) == 1 {
		totalText = "1 episode"
	}
	headerRight := st.Dim.Render(totalText)
	headerLeft := st.Bold.Render(truncate(data.SeriesTitle, max(10, listW-lipgloss.Width(headerRight)-1)))
	gap := max(1, listW-lipgloss.Width(headerLeft)-lipgloss.Width(headerRight))
	rows = append(rows, headerLeft+strings.Repeat(" ", gap)+headerRight)

	// 2. Season tab strip (only if show has > 1 season), windowed
	// to the list column so long runs never bleed into the pane.
	if data.SeasonCount > 1 {
		seasonStrip := RenderSeasonTabs(data.SeasonCount, data.ActiveSeason, data.Accent, listW)
		rows = append(rows, "", seasonStrip)
	}

	rows = append(rows, "")

	// While loading with no rows yet, the body stays header-only: the
	// fixed loading row above the footer carries the spinner, so no
	// inline line appears/disappears to shift the list, and no
	// premature "no episodes" flashes.
	if data.Loading {
		return rows
	}

	if len(data.Episodes) == 0 {
		rows = append(rows, st.Dim.Render("No episodes found for this season."))
		return rows
	}

	// Filter to active season, then by text query. SelectedIndex addresses
	// the final filtered list.
	seasonEpisodes, origIndices := filterToSeason(data.Episodes, data.SeasonCount, data.ActiveSeason)
	seasonEpisodes, origIndices = FilterEpisodesByText(seasonEpisodes, origIndices, data.FilterQuery)

	if data.Filtering || strings.TrimSpace(data.FilterQuery) != "" {
		rows = append(rows, st.Dim.Render("/")+data.FilterQuery+st.Cursor.Render("▌"))
	}

	if len(seasonEpisodes) == 0 {
		rows = append(rows, st.Dim.Render("No episodes match. (esc clears the filter)"))
		return rows
	}

	// 3. Episode rows with scroll window
	visibleHeight := data.Height - len(rows) - 3
	if visibleHeight < 5 {
		visibleHeight = 5
	}

	startIndex := windowStart(len(seasonEpisodes), data.SelectedIndex, visibleHeight)
	endIndex := min(len(seasonEpisodes), startIndex+visibleHeight)

	// 3. Episode rows with scroll window. The right column (progress or
	// runtime) is padded to one width so titles align across rows.
	multiSeason := data.SeasonCount > 1
	rightCols := make([]string, len(seasonEpisodes))
	rightW := 0
	for i, ep := range seasonEpisodes {
		rightCols[i] = episodeRightCol(ep, historyEntryFor(data, ep), data.Accent, st)
		if w := lipgloss.Width(rightCols[i]); w > rightW {
			rightW = w
		}
	}

	for i := startIndex; i < endIndex; i++ {
		ep := seasonEpisodes[i]
		isFocused := i == data.SelectedIndex
		rows = append(rows, renderEpisodeRow(ep, origIndices[i], isFocused, multiSeason, rightCols[i], rightW, data, listW, st))
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
	q := strings.ToLower(strings.TrimSpace(query))
	if q == "" {
		return episodes, idxs
	}
	var out []provider.Episode
	var outIdx []int
	for i, ep := range episodes {
		num := ep.Episode
		if num == 0 {
			num = idxs[i] + 1
		}
		hay := strings.ToLower(ep.Title) + " " + fmt.Sprintf("%d", num)
		if strings.Contains(hay, q) {
			out = append(out, ep)
			outIdx = append(outIdx, idxs[i])
		}
	}
	return out, outIdx
}

// filterToSeason returns the episodes for the active season and their original indices.
func filterToSeason(episodes []provider.Episode, seasonCount, activeSeason int) ([]provider.Episode, []int) {
	seasons := distinctSeasonNumbers(episodes)
	if len(seasons) <= 1 {
		idxs := make([]int, len(episodes))
		for i := range idxs {
			idxs[i] = i
		}
		return episodes, idxs
	}

	if activeSeason < 0 {
		activeSeason = 0
	}
	if activeSeason >= len(seasons) {
		activeSeason = len(seasons) - 1
	}
	target := seasons[activeSeason]

	var filtered []provider.Episode
	var idxs []int
	for i, ep := range episodes {
		s := ep.Season
		if s <= 0 {
			s = 1
		}
		if s == target {
			filtered = append(filtered, ep)
			idxs = append(idxs, i)
		}
	}
	if len(filtered) > 0 {
		return filtered, idxs
	}

	allIdxs := make([]int, len(episodes))
	for i := range allIdxs {
		allIdxs[i] = i
	}
	return episodes, allIdxs
}

// renderInspectorPane renders the right pane: poster + series meta + selected episode detail.
func renderInspectorPane(data EpisodesData, paneW int, st Styles) []string {
	var lines []string

	// Poster block (pre-rendered, may be multi-line)
	if data.PosterBlock != "" {
		lines = append(lines, strings.Split(data.PosterBlock, "\n")...)
		lines = append(lines, "")
	}

	// Series title
	if data.SeriesTitle != "" {
		lines = append(lines, truncate(data.SeriesTitle, paneW))
	}

	// Year · Kind · Rating
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
	if len(metaParts) > 0 {
		lines = append(lines, truncate(strings.Join(metaParts, st.Dim.Render(" · ")), paneW))
	}

	// Genres
	if len(data.SeriesGenres) > 0 {
		genreStr := strings.Join(data.SeriesGenres, " · ")
		lines = append(lines, st.Dim.Render(truncate(genreStr, paneW)))
	}

	// Overview (use shared wrapText from results.go)
	if data.SeriesOverview != "" {
		lines = append(lines, "")
		wrapped := wrapText(data.SeriesOverview, paneW, 8)
		lines = append(lines, st.Dim.Render(wrapped))
	}

	// Selected episode detail
	if data.SelectedEpisodeTitle != "" {
		lines = append(lines, "")
		lines = append(lines, truncate(data.SelectedEpisodeTitle, paneW))
	}
	if data.SelectedEpisodeAired != "" {
		lines = append(lines, st.Dim.Render("aired "+data.SelectedEpisodeAired))
	}

	return lines
}

// joinColumns merges left and right line slices side-by-side with a generous 4-char gutter.
func joinColumns(left, right []string, leftW, _ int) string {
	n := max(len(left), len(right))
	out := make([]string, n)
	for i := range out {
		var l, r string
		if i < len(left) {
			l = left[i]
		}
		if i < len(right) {
			r = right[i]
		}
		// Pad left column to leftW, then 4-char gutter, then right column
		lPad := leftW - lipgloss.Width(l)
		if lPad < 0 {
			lPad = 0
		}
		out[i] = l + strings.Repeat(" ", lPad) + "    " + r
	}
	return strings.Join(out, "\n")
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
func episodeRightCol(ep provider.Episode, historyEntry *history.Entry, accent lipgloss.AdaptiveColor, st Styles) string {
	if historyEntry != nil {
		if historyEntry.Complete || (historyEntry.DurationSecs > 0 && (historyEntry.PositionSecs/historyEntry.DurationSecs) >= 0.95) {
			return ""
		}
		if historyEntry.PositionSecs > 0 && historyEntry.DurationSecs > 0 {
			ratio := historyEntry.PositionSecs / historyEntry.DurationSecs
			progressBar := RenderProgressBar(ratio, 8, accent)
			leftSecs := historyEntry.DurationSecs - historyEntry.PositionSecs
			return progressBar + "  " + st.Dim.Render(fmt.Sprintf("%dm left", int(leftSecs/60)))
		}
		if historyEntry.DurationSecs > 0 {
			return st.Dim.Render(formatDurationShort(historyEntry.DurationSecs))
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
			titleTrunc = st.Bold.Render(titleTrunc)
		}

		return fmt.Sprintf("%s%s%s  %s", cursor, st.Dim.Render(box), st.Dim.Render(code), titleTrunc)
	}

	// Normal Mode
	cursor := "  "
	if isFocused {
		cursor = st.Cursor.Render("▌ ")
	}

	statusMarker := "  "
	if historyEntry != nil && (historyEntry.Complete || (historyEntry.DurationSecs > 0 && (historyEntry.PositionSecs/historyEntry.DurationSecs) >= 0.95)) {
		statusMarker = st.Ok.Render("✓ ")
	}

	// Pad the right column to the widest visible one so titles align.
	rightPadded := rightCol + strings.Repeat(" ", max(0, rightW-lipgloss.Width(rightCol)))

	// cursor + status + code + gap + title + gap + right: the two
	// 2-char gaps both count, so rows land exactly on listW. (A missing
	// gap here once rendered every row 4 cells wide, and the terminal
	// wrapped the overflow into the inspector pane.)
	fixedW := 2 + 2 + lipgloss.Width(code) + 2 + 2 + rightW
	titleW := listW - fixedW
	if titleW < 15 {
		titleW = 15
	}

	titleTrunc := truncate(title, titleW)
	titlePadded := titleTrunc + strings.Repeat(" ", max(0, titleW-lipgloss.Width(titleTrunc)))
	if isFocused {
		titlePadded = st.Bold.Render(titleTrunc) + strings.Repeat(" ", max(0, titleW-lipgloss.Width(titleTrunc)))
	}

	codeStr := st.Dim.Render(code)

	return fmt.Sprintf("%s%s%s  %s  %s", cursor, statusMarker, codeStr, titlePadded, rightPadded)
}
