package tui

import (
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// padCells fits one line into an exact cell width, truncating with an ellipsis
// and padding the remainder with spaces. Widths include ANSI styling.
func padCells(line string, width int) string {
	if width <= 0 {
		return ""
	}
	used := ansi.StringWidth(line)
	if used > width {
		line = ansi.Truncate(line, width, "…")
		used = ansi.StringWidth(line)
	}
	if used > width {
		used = width
	}
	return line + strings.Repeat(" ", width-used)
}

// padLines returns exactly count lines, truncating extra input and filling
// missing lines with blanks. Screen renderers use it to reserve fixed-height
// slots so asynchronously arriving data only changes text inside a slot.
func padLines(lines []string, count int) []string {
	if count <= 0 {
		return nil
	}
	out := make([]string, count)
	if len(lines) > count {
		lines = lines[:count]
	}
	copy(out, lines)
	return out
}

// wrapFixed wraps text into exactly lineCount lines of at most width cells.
// Unlike wrapText it never changes the line count as data arrives or as text
// gets shorter, which is the property fixed metadata slots need.
func wrapFixed(text string, width, lineCount int) string {
	if lineCount <= 0 {
		return ""
	}
	wrapped := wrapText(text, width, lineCount)
	var lines []string
	if wrapped != "" {
		lines = strings.Split(wrapped, "\n")
	}
	return strings.Join(padLines(lines, lineCount), "\n")
}

// fixedBlock renders content into an exact width-by-height cell block. Every
// poster slot uses it so a loading placeholder and the final artwork occupy
// the same cells and nothing below them moves when the image arrives.
func fixedBlock(content string, width, height int) string {
	if width <= 0 || height <= 0 {
		return ""
	}
	lines := strings.Split(content, "\n")
	out := make([]string, height)
	for i := range out {
		if i < len(lines) {
			out[i] = padCells(lines[i], width)
		} else {
			out[i] = strings.Repeat(" ", width)
		}
	}
	return strings.Join(out, "\n")
}

// insetFixedBlock renders content into an exact cell block with a small,
// constant left inset. Posters sit just off the left edge of their reserved
// slot — close enough to the metadata pane that the gutter feels tight, but
// not flush against the frame. The inset is constant, so arrival of artwork
// never moves a single cell.
func insetFixedBlock(content string, width, height, inset int) string {
	if width <= 0 || height <= 0 {
		return ""
	}
	if inset < 0 || inset >= width {
		inset = 0
	}
	lines := strings.Split(content, "\n")
	out := make([]string, height)
	for i := range out {
		line := ""
		if i < len(lines) {
			line = strings.TrimRight(lines[i], " ")
		}
		used := ansi.StringWidth(line)
		available := width - inset
		if used > available {
			line = ansi.Truncate(line, available, "…")
			used = ansi.StringWidth(line)
		}
		if used > available {
			used = available
		}
		out[i] = strings.Repeat(" ", inset) + line + strings.Repeat(" ", available-used)
	}
	return strings.Join(out, "\n")
}
