package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// HelpData contains keys for the active screen and global navigation.
type HelpData struct {
	ScreenName  string
	ContextKeys []KeyBinding
	GlobalKeys  []KeyBinding
	Width       int
	Accent      lipgloss.AdaptiveColor
}

func RenderHelpOverlay(data HelpData) string {
	st := NewStyles(data.Accent)

	colW := min(36, max(24, (data.Width-12)/2))

	if len(data.ContextKeys) == 0 {
		var lines []string
		lines = append(lines, st.Bold.Render("Global"), "")
		for _, b := range data.GlobalKeys {
			kStr := fmt.Sprintf("%-10s", b.Key)
			lines = append(lines, kStr+"  "+st.Dim.Render(b.Action))
		}
		singleCol := strings.Join(lines, "\n")
		boxW := colW + 6
		box := RenderOverlay(lipgloss.NewStyle().Width(colW).Render(singleCol), boxW, data.Accent)
		return box + "\n\n  " + st.Dim.Render("esc close")
	}

	// Build left column (Contextual keys)
	var leftLines []string
	leftLines = append(leftLines, st.Bold.Render(data.ScreenName), "")
	for _, b := range data.ContextKeys {
		kStr := fmt.Sprintf("%-10s", b.Key)
		leftLines = append(leftLines, kStr+"  "+st.Dim.Render(b.Action))
	}

	// Build right column (Global keys)
	var rightLines []string
	rightLines = append(rightLines, st.Bold.Render("Global"), "")
	for _, b := range data.GlobalKeys {
		kStr := fmt.Sprintf("%-10s", b.Key)
		rightLines = append(rightLines, kStr+"  "+st.Dim.Render(b.Action))
	}

	maxRows := max(len(leftLines), len(rightLines))
	for len(leftLines) < maxRows {
		leftLines = append(leftLines, "")
	}
	for len(rightLines) < maxRows {
		rightLines = append(rightLines, "")
	}

	leftCol := strings.Join(leftLines, "\n")
	rightCol := strings.Join(rightLines, "\n")

	content := lipgloss.JoinHorizontal(lipgloss.Top,
		lipgloss.NewStyle().Width(colW).Render(leftCol),
		"    ",
		lipgloss.NewStyle().Width(colW).Render(rightCol),
	)

	boxW := 2*colW + 8
	box := RenderOverlay(content, boxW, data.Accent)
	return box + "\n\n  " + st.Dim.Render("esc close")
}
