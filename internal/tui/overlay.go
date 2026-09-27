package tui

import (
	"github.com/charmbracelet/lipgloss"
)

// RenderOverlay wraps inner content in a single rounded border box with dim border.
// Per spec §2.2, this is the only place borders/boxes are permitted.
func RenderOverlay(content string, width int, accent lipgloss.AdaptiveColor) string {
	st := NewStyles(accent)
	box := st.OverlayBox
	if width > 0 {
		box = box.Width(width - 4)
	}
	return box.Render(content)
}
