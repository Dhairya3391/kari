package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

const (
	// maxContentWidth caps the centered content column. resizeLists (utils.go)
	// and ComputeDims share it so lists and frames agree on available width.
	maxContentWidth = 140
	// minContentMargin keeps content off the terminal edge.
	minContentMargin = 4
)

// Dims holds the computed layout dimensions for a terminal frame.
type Dims struct {
	TermWidth    int
	TermHeight   int
	ContentWidth int
	ContentStart int // Left padding column
	BodyHeight   int
	ShowSpacer   bool
}

// ComputeDims calculates responsive layout boundaries based on current terminal dimensions.
//
// The content column extends to the terminal width (minus gutters) until it
// reaches maxContentWidth; beyond that the column is fixed and centered.
// A minimum margin keeps text off the very edge on small terminals, and
// short side is compensated so the block reads as centered rather than
// pushed against one margin. Content is always centered: the column never
// hugs the left edge.
func ComputeDims(width, height int) Dims {
	// Centered gutters shrink as the terminal narrows, floored at
	// minContentMargin so text never touches the edge.
	gutter := (width - maxContentWidth) / 2
	if gutter < minContentMargin {
		gutter = minContentMargin
	}
	if width < 2*minContentMargin {
		gutter = width / 2 // degenerate: narrower than both margins
	}
	contentW := width - 2*gutter
	if contentW < 20 {
		contentW = 20
	}

	showSpacer := height >= 24

	reservedVertical := 2 // Header (1) + Footer (1)
	if showSpacer {
		reservedVertical++ // 1 blank spacer row below header
	}

	bodyH := height - reservedVertical
	if bodyH < 5 {
		bodyH = 5
	}

	return Dims{
		TermWidth:    width,
		TermHeight:   height,
		ContentWidth: contentW,
		ContentStart: gutter,
		BodyHeight:   bodyH,
		ShowSpacer:   showSpacer,
	}
}

// RenderFrame wraps header, content body, optional status block (toast
// and/or loading rows), and footer into the responsive 96-col
// The status block may span lines (loading + toast stacked); every line
// is budgeted so the body never shifts and the footer stays pinned.
func RenderFrame(dims Dims, header, body, toast, footer string) string {
	padStyle := lipgloss.NewStyle().MarginLeft(dims.ContentStart).Width(dims.ContentWidth)

	var rows []string

	// 1. Header (Row 1)
	rows = append(rows, padStyle.Render(header))

	// 2. Blank spacer row (if height >= 24)
	if dims.ShowSpacer {
		rows = append(rows, "")
	}

	// 3. Body
	statusLines := 0
	if toast != "" {
		statusLines = len(strings.Split(toast, "\n"))
	}
	bodyLines := strings.Split(body, "\n")
	availableBodyLines := dims.BodyHeight - statusLines

	for i := 0; i < len(bodyLines) && i < availableBodyLines; i++ {
		rows = append(rows, padStyle.Render(bodyLines[i]))
	}

	// Pad remaining vertical space so footer is pinned to the last row
	for len(rows) < dims.TermHeight-statusLines-1 {
		rows = append(rows, "")
	}

	// 4. Status block (directly above footer)
	if toast != "" {
		rows = append(rows, padStyle.Render(toast))
	}

	// 5. Footer (pinned to bottom row)
	rows = append(rows, padStyle.Render(footer))

	// Truncate to exact terminal height to avoid terminal scroll jitter
	if len(rows) > dims.TermHeight {
		rows = rows[:dims.TermHeight]
	}

	return strings.Join(rows, "\n")
}
