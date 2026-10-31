package tui

import (
	"runtime"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// KeyBinding represents a key and its human-readable action label.
type KeyBinding struct {
	Key    string
	Action string
}

type IntegrationStatus struct {
	PlayerName       string
	Playing          bool
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
		pair := formatKeyLabel(b.Key) + " " + st.Dim.Render(b.Action)
		leftParts = append(leftParts, pair)
	}
	leftContent := strings.Join(leftParts, "   ")

	// Build right-hand integration status
	rightContent := ""
	if status != nil && (status.PlayerName != "" || status.TrackerName != "") {
		var parts []string
		if status.PlayerName != "" {
			playerPart := strings.ToLower(status.PlayerName)
			if status.Playing {
				dot := st.Ok.Render("●")
				playerPart = playerPart + " " + dot
			}
			parts = append(parts, playerPart)
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

func formatKeyLabel(label string) string {
	return formatKeyLabelForOS(label, runtime.GOOS)
}

func formattedKeyBindings(bindings []KeyBinding) []KeyBinding {
	formatted := append([]KeyBinding(nil), bindings...)
	for i := range formatted {
		formatted[i].Key = formatKeyLabel(formatted[i].Key)
	}
	return formatted
}

func formatKeyLabelForOS(label, goos string) string {
	modifierLabels := []string{"ctrl+", "Ctrl+", "CTRL+", "alt+", "Alt+", "ALT+", "shift+", "Shift+", "SHIFT+"}
	for _, modifier := range modifierLabels {
		replacement := "Ctrl+"
		switch strings.ToLower(modifier) {
		case "alt+":
			replacement = "Alt+"
		case "shift+":
			replacement = "Shift+"
		}
		if goos == "darwin" {
			switch strings.ToLower(modifier) {
			case "ctrl+":
				replacement = "Control+"
			case "alt+":
				replacement = "Option+"
			case "shift+":
				replacement = "Shift+"
			}
		}
		label = strings.ReplaceAll(label, modifier, replacement)
	}
	return strings.NewReplacer(
		"tab/⇧tab", "Tab / Shift+Tab",
		"⇧tab", "Shift+Tab",
		"tab", "Tab",
		"backspace", "Backspace",
		"delete", "Delete",
		"enter", "Enter",
		"esc", "Esc",
		"space", "Space",
	).Replace(label)
}
