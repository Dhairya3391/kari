package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"kari/internal/provider"
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
	FilterQuery   string // / text filter; empty means unfiltered
	Filtering     bool   // true while the user is typing the / filter
	PosterBlock   string
	Overview      string
	Genres        []string
	Rating        string
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
}

// queryLine renders the search-box line: the live input while focused,
// the static last-submitted query otherwise.
func queryLine(data ResultsData) string {
	if data.InputFocused && data.InputView != "" {
		return data.InputView
	}
	return "› " + data.Query
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

	// While loading with no rows yet, the body stays header-only: the
	// fixed loading row above the footer carries the spinner, so no
	// inline line appears/disappears to shift content, and no premature
	// "no results" flashes.
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
			st.Dim.Render("Check the spelling, or try another mode (tab)."),
		}
		return strings.Join(emptyMsg, "\n")
	}

	showPosterPane := (data.TermWidth >= 90 || data.Width >= 70)

	if !showPosterPane {
		// Single column table spanning full width
		table := renderResultsTable(data, data.Width, data.Height, st)
		return strings.Join(append(headerLines, table), "\n")
	}

	// 2-column layout: details pane gets a compact column (up to 36 cells),
	// giving the results list the majority of space for full title visibility.
	rightW := min(36, max(26, data.Width*28/100))
	leftW := data.Width - rightW - 4
	if leftW < 40 {
		leftW = 40
		rightW = max(24, data.Width-leftW-4)
	}

	leftCol := renderResultsTable(data, leftW, data.Height, st)
	rightCol := renderResultsDetails(data, rightW, st)

	joined := lipgloss.JoinHorizontal(lipgloss.Top,
		lipgloss.NewStyle().Width(leftW).Render(leftCol),
		"    ",
		lipgloss.NewStyle().Width(rightW).Render(rightCol),
	)

	return strings.Join(append(headerLines, joined), "\n")
}

func renderResultsTable(data ResultsData, width, height int, st Styles) string {
	var rows []string

	// Calculate column widths
	yearW := 6
	fixedW := 2 + yearW + 4 // cursor + year + gap
	titleW := width - fixedW
	if titleW < 18 {
		titleW = 18
	}

	for i, item := range data.Results {
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
			titlePadded = st.Bold.Render(titleTrunc) + strings.Repeat(" ", max(0, titleW-lipgloss.Width(titleTrunc)))
		}

		yearStr := "    "
		if item.Year != "" {
			yearStr = st.Dim.Render(fmt.Sprintf("%4s", truncate(item.Year, 4)))
		}

		row := fmt.Sprintf("%s%s  %s", cursor, titlePadded, yearStr)
		rows = append(rows, row)
	}

	// 4 lines reserved above the table ("› query", count, "" + margin),
	// plus 2 more while the / filter prompt is shown.
	reserve := 4
	if data.Filtering || strings.TrimSpace(data.FilterQuery) != "" {
		reserve = 6
	}
	return strings.Join(applyWindow(rows, data.SelectedIndex, height, reserve, st), "\n")
}

func renderResultsDetails(data ResultsData, width int, st Styles) string {
	if data.SelectedIndex < 0 || data.SelectedIndex >= len(data.Results) {
		return ""
	}
	sel := data.Results[data.SelectedIndex]

	var parts []string

	// 1. Poster block (Kitty/Sixel/iTerm raw sequence)
	if data.PosterBlock != "" {
		parts = append(parts, data.PosterBlock, "")
	}

	// 2. Title
	title := sel.Title
	parts = append(parts, st.Bold.Render(title))

	// 3. Metadata line: e.g. "2019 · ★ 9.38" or "2019"
	var meta []string
	if sel.Year != "" {
		meta = append(meta, st.Dim.Render(sel.Year))
	}
	if data.Rating != "" && data.Rating != "—" {
		meta = append(meta, st.Current.Render("★ ")+st.Dim.Render(data.Rating))
	}
	if len(meta) > 0 {
		parts = append(parts, strings.Join(meta, st.Dim.Render(" · ")))
	}

	// 4. Genres line
	if len(data.Genres) > 0 {
		parts = append(parts, st.Dim.Render(strings.Join(data.Genres, " · ")))
	}

	// 5. Synopsis / Plot Overview
	if data.Overview != "" {
		parts = append(parts, "")
		wrapped := wrapText(data.Overview, width, 8)
		parts = append(parts, st.Dim.Render(wrapped))
	}

	return strings.Join(parts, "\n")
}
