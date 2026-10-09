package tui

import (
	"strings"
	"sync"

	"github.com/charmbracelet/lipgloss"
)

// Styles provides pre-configured Lipgloss styles based on the active accent color.
type Styles struct {
	Accent lipgloss.AdaptiveColor

	// Typography & Highlights
	Wordmark lipgloss.Style
	Current  lipgloss.Style
	Dim      lipgloss.Style
	Bold     lipgloss.Style
	Cursor   lipgloss.Style

	// Tabs
	ActiveTab   lipgloss.Style
	InactiveTab lipgloss.Style
	Underline   lipgloss.Style

	// Status & Indicators
	Ok  lipgloss.Style
	Err lipgloss.Style

	// Overlay & Modals
	OverlayBox lipgloss.Style

	// Form & Values
	EditableValue lipgloss.Style
}
var (
	stylesMu    sync.RWMutex
	stylesCache = make(map[lipgloss.AdaptiveColor]Styles)
)

// NewStyles constructs or retrieves a cached Styles bundle using the specified accent token.
func NewStyles(accent lipgloss.AdaptiveColor) Styles {
	stylesMu.RLock()
	st, ok := stylesCache[accent]
	stylesMu.RUnlock()
	if ok {
		return st
	}

	st = buildStyles(accent)
	stylesMu.Lock()
	stylesCache[accent] = st
	stylesMu.Unlock()
	return st
}

func buildStyles(accent lipgloss.AdaptiveColor) Styles {
	return Styles{
		Accent: accent,

		Wordmark: lipgloss.NewStyle().
			Foreground(accent).
			Bold(true),

		Current: lipgloss.NewStyle().
			Foreground(accent).
			Bold(true),

		Dim: lipgloss.NewStyle().
			Foreground(ColorDim),

		Bold: lipgloss.NewStyle().
			Bold(true),

		Cursor: lipgloss.NewStyle().
			Foreground(accent).
			Bold(true),

		ActiveTab: lipgloss.NewStyle().
			Foreground(accent).
			Bold(true),

		InactiveTab: lipgloss.NewStyle().
			Foreground(ColorDim),

		Underline: lipgloss.NewStyle().
			Foreground(accent),

		Ok: lipgloss.NewStyle().
			Foreground(ColorOk),

		Err: lipgloss.NewStyle().
			Foreground(ColorErr),

		OverlayBox: lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(ColorDim).
			Padding(1, 2),

		EditableValue: lipgloss.NewStyle().
			Foreground(accent),
	}
}
// RenderUnderline generates the horizontal underline string '─────' of given width in accent color.
func RenderUnderline(width int, accent lipgloss.AdaptiveColor) string {
	if width <= 0 {
		return ""
	}
	return NewStyles(accent).Underline.Render(strings.Repeat("─", width))
}
