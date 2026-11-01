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
	titleW := data.Width - 56
	if titleW < 20 {
		titleW = 20
	}

	type rowKind uint8
	const (
		activeHeader rowKind = iota
		activeEmpty
		blank
		activeItem
		doneHeader
		doneEmpty
		doneItem
	)
	type displayRow struct {
		kind  rowKind
		index int
	}
	flat := []displayRow{{kind: activeHeader}}
	if len(data.Active) == 0 {
		flat = append(flat, displayRow{kind: activeEmpty}, displayRow{kind: blank})
	} else {
		for i := range data.Active {
			flat = append(flat, displayRow{kind: activeItem, index: i})
		}
		flat = append(flat, displayRow{kind: blank})
	}
	doneHeaderIndex := len(flat)
	flat = append(flat, displayRow{kind: doneHeader})
	if len(data.Done) == 0 {
		flat = append(flat, displayRow{kind: doneEmpty})
	} else {
		for i := range data.Done {
			flat = append(flat, displayRow{kind: doneItem, index: i})
		}
	}

	flatCursor := -1
	if data.SelectedIndex >= 0 && data.SelectedIndex < len(data.Active) {
		flatCursor = 1 + data.SelectedIndex
	} else if doneIndex := data.SelectedIndex - len(data.Active); doneIndex >= 0 && doneIndex < len(data.Done) {
		flatCursor = doneHeaderIndex + 1 + doneIndex
	}
	start, end, above, below := 0, len(flat), 0, 0
	if data.Height > 0 {
		start, end, above, below = windowRange(len(flat), flatCursor, data.Height, 0)
	}
	var rows []string
	if hint := windowHint(above, true, st); hint != "" {
		rows = append(rows, hint)
	}
	for _, row := range flat[start:end] {
		switch row.kind {
		case activeHeader:
			rows = append(rows, st.Dim.Render(fmt.Sprintf("Active · %d", len(data.Active))))
		case activeEmpty:
			rows = append(rows, "  "+st.Dim.Render("no active downloads"))
		case blank:
			rows = append(rows, "")
		case activeItem:
			rows = append(rows, renderActiveDownload(data.Active[row.index], row.index == data.SelectedIndex, titleW, data, st))
		case doneHeader:
			rows = append(rows, st.Dim.Render(fmt.Sprintf("Done · %d", len(data.Done))))
		case doneEmpty:
			rows = append(rows, "  "+st.Dim.Render("no completed downloads"))
		case doneItem:
			rows = append(rows, renderDoneDownload(data.Done[row.index], len(data.Active)+row.index == data.SelectedIndex, titleW, st))
		}
	}
	if hint := windowHint(below, false, st); hint != "" {
		rows = append(rows, hint)
	}
	return strings.Join(rows, "\n")
}

func renderActiveDownload(item ActiveDownload, isFocused bool, titleW int, data DownloadsData, st Styles) string {
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
		return fmt.Sprintf("%s%s  %s  %s", cursor, titleStr, qualityStr, st.Dim.Render("queued"))
	}
	bar := RenderProgressBar(item.Progress, 10, data.Accent)
	pctStr := st.Current.Render(fmt.Sprintf("%3.0f%%", item.Progress*100))
	if item.Paused {
		return fmt.Sprintf("%s%s  %s  %s   %s   %s", cursor, titleStr, qualityStr, bar, pctStr, st.Dim.Render("paused"))
	}
	speed, eta := item.Speed, item.ETA
	if item.Progress == 0 && speed == "" && eta == "" {
		speed = "connecting…"
	}
	return fmt.Sprintf("%s%s  %s  %s   %s   %s   %s", cursor, titleStr, qualityStr, bar, pctStr, st.Dim.Render(fmt.Sprintf("%-10s", speed)), st.Dim.Render(fmt.Sprintf("%-8s", eta)))
}

func renderDoneDownload(item DoneDownload, isFocused bool, titleW int, st Styles) string {
	cursor := "  "
	if isFocused {
		cursor = st.Cursor.Render("▌ ")
	}
	titleTrunc := truncate(item.Title, titleW)
	titleStr := titleTrunc + strings.Repeat(" ", max(0, titleW-lipgloss.Width(titleTrunc)))
	if isFocused {
		titleStr = st.Current.Render(titleTrunc) + strings.Repeat(" ", max(0, titleW-lipgloss.Width(titleTrunc)))
	}
	quality := fmt.Sprintf("%-6s", truncate(item.Quality, 6))
	qualityStr := st.Dim.Render(quality)
	if isFocused {
		qualityStr = st.Current.Render(quality)
	}
	return fmt.Sprintf("%s%s  %s  %s %s", cursor, titleStr, qualityStr, st.Ok.Render("✓ "), st.Dim.Render(item.Size))
}
