package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"kari/internal/provider"
)

// RenderModePeek renders the transient mode peek row on the toast line per spec §2:
// `‹ cartoon      ANIME      live ›`
// (dim neighbors, current in UPPERCASE + accent bold).
// If there is only 1 or 0 effective modes, returns empty string.
func RenderModePeek(modes []provider.ContentType, activeMode provider.ContentType, accent lipgloss.AdaptiveColor, width int) string {
	if len(modes) <= 1 {
		return ""
	}

	st := NewStyles(accent)

	// Find active index
	activeIdx := 0
	for i, m := range modes {
		if m == activeMode {
			activeIdx = i
			break
		}
	}

	// Neighbor before and after with wrapping
	prevIdx := (activeIdx - 1 + len(modes)) % len(modes)
	nextIdx := (activeIdx + 1) % len(modes)

	prevLabel := st.Dim.Render(strings.ToLower(string(modes[prevIdx])))
	currLabel := st.Current.Render(strings.ToUpper(string(modes[activeIdx])))
	nextLabel := st.Dim.Render(strings.ToLower(string(modes[nextIdx])))

	inner := fmt.Sprintf("%s      %s      %s", prevLabel, currLabel, nextLabel)
	peekStr := fmt.Sprintf("‹ %s ›", inner)

	// Center in available width
	if width > lipgloss.Width(peekStr) {
		pad := (width - lipgloss.Width(peekStr)) / 2
		return strings.Repeat(" ", pad) + peekStr
	}

	return peekStr
}
