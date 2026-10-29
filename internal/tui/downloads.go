package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// ActiveDownload represents an in-progress or queued download job.
type ActiveDownload struct {
	Title      string
	Quality    string
	Progress   float64 // 0.0 - 1.0
	Speed      string  // e.g. "3.2 MB/s"
	ETA        string  // e.g. "4m left"
	Downloaded string  // e.g. "450 MB"
	TotalSize  string  // e.g. "1.2 GB"
	Queued     bool
	Paused     bool
}

// DoneDownload represents a successfully completed download.
type DoneDownload struct {
	Title   string
	Quality string
	Size    string // e.g. "3.05 GB"
	Path    string
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
				titleStr = st.Current.Render(titleTrunc) + strings.Repeat(" ", max(0, titleW-lipgloss.Width(titleTrunc)))
			}

			qFormatted := fmt.Sprintf("%-6s", truncate(item.Quality, 6))
			qualityStr := st.Dim.Render(qFormatted)
			if isFocused {
				qualityStr = st.Current.Render(qFormatted)
			}

			if item.Queued {
				rows = append(rows, fmt.Sprintf("%s%s  %s  %s", cursor, titleStr, qualityStr, st.Dim.Render("queued")))
			} else if item.Paused {
				bar := RenderProgressBar(item.Progress, 10, data.Accent)
				pctStr := st.Current.Render(fmt.Sprintf("%3.0f%%", item.Progress*100))
				rows = append(rows, fmt.Sprintf("%s%s  %s  %s   %s   %s", cursor, titleStr, qualityStr, bar, pctStr, st.Dim.Render("paused")))
			} else {
				bar := RenderProgressBar(item.Progress, 10, data.Accent)
				pctStr := st.Current.Render(fmt.Sprintf("%3.0f%%", item.Progress*100))
				speed := item.Speed
				eta := item.ETA
				if item.Progress == 0 && speed == "" && eta == "" {
					speed = "connecting…"
				}
				speedStr := fmt.Sprintf("%-10s", speed)
				etaStr := fmt.Sprintf("%-8s", eta)

				rows = append(rows, fmt.Sprintf("%s%s  %s  %s   %s   %s   %s", cursor, titleStr, qualityStr, bar, pctStr, st.Dim.Render(speedStr), st.Dim.Render(etaStr)))
			}
		}
		rows = append(rows, "")
	}

	doneHeaderIdx := len(rows)
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
				titleStr = st.Current.Render(titleTrunc) + strings.Repeat(" ", max(0, titleW-lipgloss.Width(titleTrunc)))
			}

			qFormatted := fmt.Sprintf("%-6s", truncate(item.Quality, 6))
			qualityStr := st.Dim.Render(qFormatted)
			if isFocused {
				qualityStr = st.Current.Render(qFormatted)
			}
			checkStr := st.Ok.Render("✓ ")
			sizeStr := st.Dim.Render(item.Size)

			rows = append(rows, fmt.Sprintf("%s%s  %s  %s %s", cursor, titleStr, qualityStr, checkStr, sizeStr))
		}
	}

	flatCursor := -1
	totalItems := len(data.Active) + len(data.Done)
	if totalItems > 0 && data.SelectedIndex >= 0 {
		if data.SelectedIndex < len(data.Active) {
			flatCursor = 1 + data.SelectedIndex
		} else {
			doneIdx := data.SelectedIndex - len(data.Active)
			if doneIdx < len(data.Done) {
				flatCursor = doneHeaderIdx + 1 + doneIdx
			}
		}
	}

	if data.Height > 0 {
		rows = applyWindow(rows, flatCursor, data.Height, 0, st)
	}
	return strings.Join(rows, "\n")
}
