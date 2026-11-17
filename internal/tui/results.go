package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"

	"kari/internal/provider"
	"kari/internal/service"
)

// ResultsData holds the state needed to render the Results screen.
type ResultsData struct {
	Query string
	// InputView is the live search-box rendering while the query input
	// is focused (space from results); InputFocused selects it over the
	// static last-submitted Query. Without this, keystrokes after space
	// update an invisible box.
	InputView     string
	InputFocused  bool
	Mode          provider.ContentType
	Results       []provider.SearchResult
	SelectedIndex int
	// ProviderNames maps a result's Provider ID to its user-facing alias.
	// Live rows use it to name the integration serving each channel.
	ProviderNames map[string]string
	FilterQuery   string // / text filter; empty means unfiltered
	Filtering     bool   // true while the user is typing the / filter
	PosterBlock   string
	Genres        []string
	Rating        string
	// CardHeight is the unshrunken body height used to size the poster slot.
	// It is independent of the transient status/loading row so the metadata
	// below the poster never shifts when that row appears or disappears.
	// Zero means fall back to Height (callers that build data directly).
	CardHeight int
	// Loading is true while an async operation (e.g. episode fetch) is
	// running on top of the results list. SpinnerFrame and LoadingText
	// supply the inline feedback per spec §4.10.
	Loading      bool
	LoadingText  string
	SpinnerFrame string
	Width        int
	TermWidth    int
	Height       int
	Accent       lipgloss.AdaptiveColor

	// LiveGuide is the prepared section layout, set only in live mode.
	LiveGuide *service.LiveGuide
	// LiveUpdatedAt is the completion time of the last live search.
	LiveUpdatedAt time.Time
	// LiveSources counts integrations that contributed to the live search.
	LiveSources int
}

// queryLine renders the search-box line: the live input while focused,
// the static last-submitted query otherwise.
func queryLine(data ResultsData) string {
	if data.InputFocused && data.InputView != "" {
		return data.InputView
	}
	return "› " + data.Query
}

func mediaTypeLabel(mediaType string) string {
	switch mediaType {
	case provider.MediaTypeMovie:
		return "Movie"
	case provider.MediaTypeTV:
		return "TV"
	case provider.MediaTypeAnime:
		return "Anime"
	case provider.MediaTypeCartoon:
		return "Cartoon"
	case provider.MediaTypeManga:
		return "Manga"
	case provider.MediaTypeNovel:
		return "Novel"
	case provider.MediaTypeComic:
		return "Comic"
	case provider.MediaTypeLive:
		return "Live"
	default:
		return ""
	}
}

// RenderResultsScreen renders the Results screen per KARI_TUI_SPEC §4.2.
// For the Live TV/Sports mode it delegates to RenderLiveResultsScreen (plan §5.5).
func RenderResultsScreen(data ResultsData) string {
	if data.Mode == provider.ModeLive {
		return RenderLiveResultsScreen(data)
	}
	st := NewStyles(data.Accent)

	// Header lines
	countText := fmt.Sprintf("%d results", len(data.Results))
	if len(data.Results) == 1 {
		countText = "1 result"
	}

	headerLines := []string{
		queryLine(data),
		st.Dim.Render(countText),
		"",
	}

	// Active / filter prompt, mirroring the episodes screen.
	if data.Filtering || strings.TrimSpace(data.FilterQuery) != "" {
		headerLines = append(headerLines, st.Dim.Render("/")+data.FilterQuery+st.Cursor.Render("▌"), "")
	}

	if len(data.Results) == 0 && !data.Loading {
		modeName := strings.ToLower(string(data.Mode))
		if strings.TrimSpace(data.FilterQuery) != "" {
			lines := []string{
				queryLine(data),
				"",
				st.Dim.Render("/") + data.FilterQuery + st.Cursor.Render("▌"),
				"",
				"No titles match.",
				st.Dim.Render("(esc clears the filter)"),
			}
			return strings.Join(lines, "\n")
		}
		emptyMsg := []string{
			queryLine(data),
			"",
			fmt.Sprintf("No results in %s.", modeName),
			st.Dim.Render("Check the spelling, or try another mode (tab / shift+tab)."),
		}
		return strings.Join(emptyMsg, "\n")
	}

	showPosterPane := (data.TermWidth >= 90 || data.Width >= 70)

	if !showPosterPane {
		// Single column table spanning full width
		table := renderResultsTable(data, data.Width, data.Height, st)
		return strings.Join(append(headerLines, table), "\n")
	}

	rightW := min(36, max(26, data.Width*28/100))
	leftW := data.Width - rightW - 4
	if leftW < 40 {
		leftW = 40
		rightW = max(24, data.Width-leftW-4)
	}

	cardHeight := data.CardHeight
	if cardHeight <= 0 {
		cardHeight = data.Height
	}
	bodyHeight := max(5, cardHeight-len(headerLines))
	// renderResultsTable reserves the header rows itself via windowRange's
	// headerReserve; subtracting them here too made the two-column list
	// several rows shorter than the single-column one.
	leftLines := strings.Split(renderResultsTable(data, leftW, data.Height, st), "\n")
	rightLines := strings.Split(renderResultsDetails(data, rightW, bodyHeight, st), "\n")
	lines := max(len(leftLines), len(rightLines))
	leftLines, rightLines = padLines(leftLines, lines), padLines(rightLines, lines)
	joined := lipgloss.JoinHorizontal(lipgloss.Top,
		lipgloss.NewStyle().Width(leftW).Render(strings.Join(leftLines, "\n")),
		strings.Repeat(" ", posterGutter),
		lipgloss.NewStyle().Width(rightW).Render(strings.Join(rightLines, "\n")),
	)

	return strings.Join(append(headerLines, joined), "\n")
}

func renderResultsTable(data ResultsData, width, height int, st Styles) string {
	var rows []string

	showType := data.Mode != provider.ModeTV && data.Mode != provider.ModeMovies
	typeW := 7
	yearW := 4
	fixedW := 2 + yearW + 2
	if showType {
		fixedW += typeW + 2
	}
	titleW := max(8, width-fixedW)

	reserve := 3
	if data.Filtering || strings.TrimSpace(data.FilterQuery) != "" {
		reserve = 5
	}
	start, end, above, below := windowRange(len(data.Results), data.SelectedIndex, height, reserve)
	if hint := windowHint(above, true, st); hint != "" {
		rows = append(rows, hint)
	}

	for i := start; i < end; i++ {
		item := data.Results[i]
		isFocused := i == data.SelectedIndex

		cursor := "  "
		if isFocused {
			cursor = st.Cursor.Render("▌ ")
		}

		title := item.Title
		if title == "" {
			title = "—"
		}
		titleTrunc := truncate(title, titleW)
		titlePadded := titleTrunc + strings.Repeat(" ", max(0, titleW-lipgloss.Width(titleTrunc)))
		if isFocused {
			titlePadded = st.Current.Render(titleTrunc) + strings.Repeat(" ", max(0, titleW-lipgloss.Width(titleTrunc)))
		}

		var typeStr string
		if showType {
			typeStr = fmt.Sprintf("%-*s", typeW, truncate(mediaTypeLabel(item.MediaType), typeW))
			if isFocused {
				typeStr = st.Current.Render(typeStr)
			} else {
				typeStr = st.Dim.Render(typeStr)
			}
		}

		yearStr := "    "
		if item.Year != "" {
			y := fmt.Sprintf("%-4s", truncate(item.Year, 4))
			if isFocused {
				yearStr = st.Current.Render(y)
			} else {
				yearStr = st.Dim.Render(y)
			}
		}
		row := fmt.Sprintf("%s%s", cursor, titlePadded)
		if showType {
			row += "  " + typeStr
		}
		row += "  " + yearStr
		rows = append(rows, row)
	}

	if hint := windowHint(below, false, st); hint != "" {
		rows = append(rows, hint)
	}
	return strings.Join(rows, "\n")
}

// renderResultsDetails fills a fixed-height details pane. Every field owns a
// slot from the first frame, so the poster, rating, genres, and synopsis
// arriving asynchronously never move the result list beside it.
func renderResultsDetails(data ResultsData, width, height int, st Styles) string {
	posterRows := detailsPosterRows(height)
	paneRows := posterRows + 14
	if data.SelectedIndex < 0 || data.SelectedIndex >= len(data.Results) {
		return strings.Join(make([]string, paneRows), "\n")
	}
	sel := data.Results[data.SelectedIndex]

	parts := append(strings.Split(fixedBlock(data.PosterBlock, width, posterRows), "\n"), "", "")

	title := sel.Title
	if title == "" {
		title = "—"
	}
	parts = append(parts, st.Bold.Render(truncate(title, width)))

	var meta []string
	if sel.Year != "" {
		meta = append(meta, st.Dim.Render(sel.Year))
	}
	if data.Rating != "" && data.Rating != "—" {
		meta = append(meta, st.Current.Render("★ ")+st.Dim.Render(data.Rating))
	}
	if len(meta) == 0 {
		parts = append(parts, "")
	} else {
		parts = append(parts, truncate(strings.Join(meta, st.Dim.Render(" · ")), width))
	}

	if len(data.Genres) == 0 {
		parts = append(parts, "")
	} else {
		parts = append(parts, st.Dim.Render(truncate(strings.Join(data.Genres, " · "), width)))
	}

	return strings.Join(padLines(parts, paneRows), "\n")
}
