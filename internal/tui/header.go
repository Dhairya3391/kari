package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// RenderHeader builds the standard header line: 'kari › crumb › crumb'.
// 'kari' is styled accent + bold, ancestor crumbs are dim, and the active crumb is normal text.
func RenderHeader(crumbs []string, accent lipgloss.AdaptiveColor, width int) string {
	return RenderHeaderWithStatus(crumbs, accent, width, "")
}

// RenderHeaderWithStatus builds the standard header line with an optional
// right-aligned status badge (e.g. active download progress).
func RenderHeaderWithStatus(crumbs []string, accent lipgloss.AdaptiveColor, width int, rightBadge string) string {
	if len(crumbs) == 0 {
		crumbs = []string{"kari"}
	}

	st := NewStyles(accent)
	sep := st.Dim.Render(" › ")

	parts := make([]string, 0, len(crumbs))
	for i, c := range crumbs {
		text := strings.ToLower(strings.TrimSpace(c))
		if i == 0 {
			parts = append(parts, st.Wordmark.Render("kari"))
		} else if i == len(crumbs)-1 {
			// Terminal default foreground (adaptive; works in both dark and light terminals).
			parts = append(parts, lipgloss.NewStyle().Render(text))
		} else {
			parts = append(parts, st.Dim.Render(text))
		}
	}

	header := strings.Join(parts, sep)
	if rightBadge == "" {
		if width > 0 && lipgloss.Width(header) > width {
			return header[:max(0, width-1)] + "…"
		}
		return header
	}

	badgeW := lipgloss.Width(rightBadge)
	availLeft := width - badgeW - 2
	if availLeft < 15 {
		return rightBadge
	}
	if lipgloss.Width(header) > availLeft {
		header = header[:max(0, availLeft-1)] + "…"
	}
	gap := max(1, width-lipgloss.Width(header)-badgeW)
	return header + strings.Repeat(" ", gap) + rightBadge
}
