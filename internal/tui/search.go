package tui

import (
	"github.com/charmbracelet/lipgloss"

	"kari/internal/provider"
)

// SearchData contains state needed to render the Search (Home) screen.
type SearchData struct {
	Modes        []provider.ContentType
	ActiveMode   provider.ContentType
	InputView    string
	InputFocused bool
	// Loading/LoadingText/SpinnerFrame are kept for the screen contract
	// but render nothing: loading feedback lives in the fixed row above
	// the footer (see renderMainView), never inline here, so its
	// appearance shifts nothing.
	Loading      bool
	LoadingText  string
	SpinnerFrame string
	Width        int
	Accent       lipgloss.AdaptiveColor
}

// RenderSearchScreen renders the Search Home screen per KARI_TUI_SPEC §4.1.
func RenderSearchScreen(data SearchData) string {
	return data.InputView
}
