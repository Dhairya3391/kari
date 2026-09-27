package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// ActiveDownload represents an in-progress or queued download job.
type ActiveDownload struct {
	Title    string
	Quality  string
	Progress float64 // 0.0 - 1.0
	Speed    string  // e.g. "3.2 MB/s"
	ETA      string  // e.g. "4m left"
	Queued   bool
	Paused   bool
}

// DoneDownload represents a successfully completed download.
type DoneDownload struct {
	Title   string
	Quality string
	Size    string // e.g. "3.05 GB"
}

// DownloadsData carries state needed to render the Downloads queue view.
type DownloadsData struct {
	Active        []ActiveDownload
	Done          []DoneDownload
	SelectedIndex int
	Width         int
	Height        int
	Accent        lipgloss.AdaptiveColor
}

// RenderDownloadsScreen renders the Downloads screen per KARI_TUI_SPEC §4.8.
func RenderDownloadsScreen(data DownloadsData) string {
	st := NewStyles(data.Accent)

	var rows []string

	titleW := data.Width - 56
	if titleW < 20 {
		titleW = 20
	}

	// Active Section
	rows = append(rows, st.Dim.Render("Active"))

	if len(data.Active) == 0 {
		rows = append(rows, "  "+st.Dim.Render("no active downloads"), "")
	} else {
		for i, item := range data.Active {
			isFocused := i == data.SelectedIndex
			cursor := "  "
			if isFocused {
				cursor = st.Cursor.Render("▌ ")
			}

			titleTrunc := truncate(item.Title, titleW)
			titleStr := titleTrunc + strings.Repeat(" ", max(0, titleW-lipgloss.Width(titleTrunc)))
			if isFocused {
				titleStr = st.Bold.Render(titleTrunc) + strings.Repeat(" ", max(0, titleW-lipgloss.Width(titleTrunc)))
			}

			qualityStr := st.Dim.Render(fmt.Sprintf("%-6s", truncate(item.Quality, 6)))

			if item.Queued {
				rows = append(rows, fmt.Sprintf("%s%s  %s  %s", cursor, titleStr, qualityStr, st.Dim.Render("queued")))
			} else if item.Paused {
				bar := RenderProgressBar(item.Progress, 10, data.Accent)
				pctStr := st.Current.Render(fmt.Sprintf("%3.0f%%", item.Progress*100))
				rows = append(rows, fmt.Sprintf("%s%s  %s  %s   %s   %s", cursor, titleStr, qualityStr, bar, pctStr, st.Dim.Render("paused")))
			} else {
				bar := RenderProgressBar(item.Progress, 10, data.Accent)
				pctStr := st.Current.Render(fmt.Sprintf("%3.0f%%", item.Progress*100))
				speedStr := fmt.Sprintf("%-10s", item.Speed)
				etaStr := fmt.Sprintf("%-8s", item.ETA)

				rows = append(rows, fmt.Sprintf("%s%s  %s  %s   %s   %s   %s", cursor, titleStr, qualityStr, bar, pctStr, st.Dim.Render(speedStr), st.Dim.Render(etaStr)))
			}
		}
		rows = append(rows, "")
	}

	// Done Section
	rows = append(rows, st.Dim.Render("Done"))

	if len(data.Done) == 0 {
		rows = append(rows, "  "+st.Dim.Render("no completed downloads"))
	} else {
		offset := len(data.Active)
		for i, item := range data.Done {
			isFocused := (offset + i) == data.SelectedIndex
			cursor := "  "
			if isFocused {
				cursor = st.Cursor.Render("▌ ")
			}

			titleTrunc := truncate(item.Title, titleW)
			titleStr := titleTrunc + strings.Repeat(" ", max(0, titleW-lipgloss.Width(titleTrunc)))
			if isFocused {
				titleStr = st.Bold.Render(titleTrunc) + strings.Repeat(" ", max(0, titleW-lipgloss.Width(titleTrunc)))
			}

			qualityStr := st.Dim.Render(fmt.Sprintf("%-6s", truncate(item.Quality, 6)))
			checkStr := st.Ok.Render("✓ ")
			sizeStr := st.Dim.Render(item.Size)

			rows = append(rows, fmt.Sprintf("%s%s  %s  %s %s", cursor, titleStr, qualityStr, checkStr, sizeStr))
		}
	}
	return strings.Join(rows, "\n")
}
