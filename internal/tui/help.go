package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// HelpData contains keys for the active screen and global navigation.
type HelpData struct {
	ScreenName  string
	ContextKeys []KeyBinding
	GlobalKeys  []KeyBinding
	Width       int
	// Scroll is the number of content lines hidden above the viewport. It is
	// clamped by the caller; positive values reveal lower key bindings.
	Scroll int
	Accent lipgloss.AdaptiveColor
}

// RenderHelpOverlay draws the scrollable keybinding help box for data.ScreenName.
func RenderHelpOverlay(data HelpData) string {
	st := NewStyles(data.Accent)
	data.ContextKeys = formattedKeyBindings(data.ContextKeys)
	data.GlobalKeys = formattedKeyBindings(data.GlobalKeys)

	availWidth := data.Width - 8
	if availWidth < 30 {
		availWidth = 30
	}

	if len(data.ContextKeys) == 0 {
		maxKey := 0
		for _, b := range data.GlobalKeys {
			if w := lipgloss.Width(b.Key); w > maxKey {
				maxKey = w
			}
		}
		if maxKey < 8 {
			maxKey = 8
		}

		var lines []string
		lines = append(lines, st.Bold.Render("Global"), "")
		for _, b := range data.GlobalKeys {
			pad := max(0, maxKey-lipgloss.Width(b.Key))
			kStr := b.Key + strings.Repeat(" ", pad)
			lines = append(lines, st.Current.Render(kStr)+"  "+st.Dim.Render(b.Action))
		}
		singleCol := strings.Join(lines, "\n")
		boxW := min(availWidth+4, max(36, maxKey+24))
		return RenderOverlay(scrollHelpContent(singleCol, data.Scroll), boxW, data.Accent)
	}

	// 1. Measure max key lengths
	maxLeftKey := 0
	for _, b := range data.ContextKeys {
		if w := lipgloss.Width(b.Key); w > maxLeftKey {
			maxLeftKey = w
		}
	}
	if maxLeftKey < 8 {
		maxLeftKey = 8
	}

	maxRightKey := 0
	for _, b := range data.GlobalKeys {
		if w := lipgloss.Width(b.Key); w > maxRightKey {
			maxRightKey = w
		}
	}
	if maxRightKey < 8 {
		maxRightKey = 8
	}

	// 2. Measure max line widths
	maxLeftWidth := lipgloss.Width(data.ScreenName)
	for _, b := range data.ContextKeys {
		w := maxLeftKey + 2 + lipgloss.Width(b.Action)
		if w > maxLeftWidth {
			maxLeftWidth = w
		}
	}

	maxRightWidth := lipgloss.Width("Global")
	for _, b := range data.GlobalKeys {
		w := maxRightKey + 2 + lipgloss.Width(b.Action)
		if w > maxRightWidth {
			maxRightWidth = w
		}
	}

	// Determine column widths
	colGap := 4
	totalContentWidth := maxLeftWidth + colGap + maxRightWidth
	colLeftW := maxLeftWidth
	colRightW := maxRightWidth

	// If total exceeds available width, scale column widths proportionally
	if totalContentWidth > availWidth {
		half := max(20, (availWidth-colGap)/2)
		colLeftW = min(maxLeftWidth, half)
		colRightW = min(maxRightWidth, availWidth-colGap-colLeftW)
	}

	// Format left column lines
	var leftLines []string
	leftLines = append(leftLines, st.Bold.Render(data.ScreenName), "")
	for _, b := range data.ContextKeys {
		pad := max(0, maxLeftKey-lipgloss.Width(b.Key))
		kStr := b.Key + strings.Repeat(" ", pad)
		availAction := max(10, colLeftW-maxLeftKey-2)
		action := truncate(b.Action, availAction)
		leftLines = append(leftLines, st.Current.Render(kStr)+"  "+st.Dim.Render(action))
	}

	// Format right column lines
	var rightLines []string
	rightLines = append(rightLines, st.Bold.Render("Global"), "")
	for _, b := range data.GlobalKeys {
		pad := max(0, maxRightKey-lipgloss.Width(b.Key))
		kStr := b.Key + strings.Repeat(" ", pad)
		availAction := max(10, colRightW-maxRightKey-2)
		action := truncate(b.Action, availAction)
		rightLines = append(rightLines, st.Current.Render(kStr)+"  "+st.Dim.Render(action))
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
		lipgloss.NewStyle().Width(colLeftW).Render(leftCol),
		strings.Repeat(" ", colGap),
		lipgloss.NewStyle().Width(colRightW).Render(rightCol),
	)

	boxW := colLeftW + colGap + colRightW + 8
	if data.Width > 0 && boxW > data.Width {
		boxW = data.Width
	}
	return RenderOverlay(scrollHelpContent(content, data.Scroll), boxW, data.Accent)
}

// helpOverlayFrameLines is what RenderOverlay wraps around the help content:
// a top and bottom border plus a top and bottom padding line.
const helpOverlayFrameLines = 4

// clampHelpScroll keeps the help overlay from scrolling past its last line,
// where it would otherwise show a nearly empty box.
func (m *modelImpl) clampHelpScroll() {
	// Measure the unscrolled box. A bound taken from the current scroll shrinks
	// as the scroll grows, which would cap it near half the content and make
	// the lower keybindings unreachable.
	body := m.renderHelpOverlayContentAtScroll(m.width, m.currentAccent(), 0)
	contentLines := max(0, strings.Count(body, "\n")+1-helpOverlayFrameLines)
	m.helpScroll = min(max(0, m.helpScroll), contentLines)
}

// scrollHelpContent drops the first scroll lines of the help body so the
// overlay can reveal lower bindings without changing its box geometry.
func scrollHelpContent(content string, scroll int) string {
	if scroll <= 0 {
		return content
	}
	lines := strings.Split(content, "\n")
	if scroll >= len(lines) {
		return strings.Join(lines[len(lines)-1:], "\n")
	}
	return strings.Join(lines[scroll:], "\n")
}
