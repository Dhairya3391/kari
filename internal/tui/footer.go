package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// KeyBinding represents a key and its human-readable action label.
type KeyBinding struct {
	Key    string
	Action string
}

// IntegrationStatus describes the current player and account tracker status.
type IntegrationStatus struct {
	PlayerName       string
	TrackerName      string
	TrackerConnected bool
}

// RenderFooter builds the standard footer line with contextual keybindings on the left
// and the player/tracker connection status on the right.
func RenderFooter(bindings []KeyBinding, status *IntegrationStatus, accent lipgloss.AdaptiveColor, width int) string {
	st := NewStyles(accent)

	// Build left-hand key action pairs
	var leftParts []string
	for _, b := range bindings {
		if b.Key == "" && b.Action == "" {
			continue
		}
		pair := b.Key + " " + st.Dim.Render(b.Action)
		leftParts = append(leftParts, pair)
	}
	leftContent := strings.Join(leftParts, "   ")

	// Build right-hand integration status
	rightContent := ""
	if status != nil && (status.PlayerName != "" || status.TrackerName != "") {
		var parts []string
		if status.PlayerName != "" {
			parts = append(parts, strings.ToLower(status.PlayerName))
		}
		if status.TrackerName != "" {
			dot := st.Dim.Render("○")
			if status.TrackerConnected {
				dot = st.Ok.Render("●")
			}
			parts = append(parts, strings.ToLower(status.TrackerName)+" "+dot)
		}
		rightContent = st.Dim.Render(strings.Join(parts, " · "))
	}

	leftW := lipgloss.Width(leftContent)
	rightW := lipgloss.Width(rightContent)

	if rightW > 0 && leftW+rightW+4 <= width {
		gap := width - leftW - rightW
		return leftContent + strings.Repeat(" ", gap) + rightContent
	}

	// If right side doesn't fit, drop right side
	if leftW <= width {
		return leftContent
	}

	// If left side is too wide, fit as many pairs as possible
	visible := []string{}
	for _, part := range leftParts {
		candidate := strings.Join(append(visible, part), "   ")
		if lipgloss.Width(candidate) > width {
			break
		}
		visible = append(visible, part)
	}
	return strings.Join(visible, "   ")
}
