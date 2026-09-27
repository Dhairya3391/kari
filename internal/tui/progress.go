package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// RenderProgressBar builds a compact progress bar of the given cell width.
// Uses '▰' for filled segments and '▱' for empty segments.
// Filled segments are styled in accent color; empty segments in dim color.
// Cells round to nearest, so the fill stays proportional to the ratio:
// zero progress never lights a cell, full progress fills all of them.
func RenderProgressBar(ratio float64, totalCells int, accent lipgloss.AdaptiveColor) string {
	if totalCells <= 0 {
		return ""
	}
	if ratio < 0 {
		ratio = 0
	}
	if ratio > 1 {
		ratio = 1
	}

	filledCells := int(ratio*float64(totalCells) + 0.5)
	if filledCells > totalCells {
		filledCells = totalCells
	}
	emptyCells := totalCells - filledCells
	if emptyCells < 0 {
		emptyCells = 0
	}

	st := NewStyles(accent)
	filledStr := st.Current.Render(strings.Repeat("▰", filledCells))
	emptyStr := st.Dim.Render(strings.Repeat("▱", emptyCells))

	return filledStr + emptyStr
}
