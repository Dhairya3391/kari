package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// ChaptersData carries state needed to render the Chapters screen.
type ChaptersData struct {
	SeriesTitle   string
	Genres        []string
	ProviderName  string
	ChaptersCount int
	Overview      string
	ListView      string
	Width         int
	Height        int
	Accent        lipgloss.AdaptiveColor
}

const chaptersStoryMaxLines = 3

// RenderChaptersScreen renders the manga/comic chapter picker screen as a single stacked column.
func RenderChaptersScreen(data ChaptersData) string {
	st := NewStyles(data.Accent)

	title := "Chapters"
	if data.SeriesTitle != "" {
		title = data.SeriesTitle
	}

	rows := []string{
		st.Dim.Render("← ") + st.Bold.Render(truncate(title, data.Width-12)),
		st.Dim.Render(fmt.Sprintf("%d chapters · %s", data.ChaptersCount, data.ProviderName)),
	}
	if len(data.Genres) > 0 {
		rows = append(rows, st.Dim.Render(truncate(strings.Join(data.Genres, " · "), data.Width)))
	}

	if data.ChaptersCount == 0 {
		rows = append(rows, "", st.Dim.Render("No chapters available."))
		return strings.Join(rows, "\n")
	}
	rows = append(rows, "", st.Dim.Render("enter read/open · / to filter · g/G top/bottom"), "")

	// Story synopsis block at the bottom
	var story []string
	if strings.TrimSpace(data.Overview) != "" {
		story = []string{
			"",
			st.Bold.Render("Story"),
			st.Dim.Render(wrapText(data.Overview, data.Width, chaptersStoryMaxLines)),
		}
	}

	if data.ListView != "" {
		rows = append(rows, data.ListView)
	}
	if len(story) > 0 {
		rows = append(rows, story...)
	}

	return strings.Join(rows, "\n")
}
