// Package termimg detects terminal image-rendering capability and renders
// images either through the Kitty graphics protocol (crisp real pixels, on
// terminals that support it) or as quadrant-block truecolor ANSI art
// everywhere else. See render.go and kitty.go for why each needs special
// handling to stay compatible with Bubble Tea's line-based redraw model.
package termimg

import (
	"github.com/BourgeoisBear/rasterm"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

// Protocol identifies how images should be rendered in the current terminal.
type Protocol int

const (
	// ProtocolNone means the terminal cannot render images at all; callers
	// should fall back to text-only output.
	ProtocolNone Protocol = iota
	// ProtocolBlocks renders images as quadrant-block truecolor ANSI art.
	ProtocolBlocks
	// ProtocolKitty renders images via the Kitty graphics protocol.
	ProtocolKitty
)

// Detect probes the current terminal once and returns the best available
// image rendering protocol. It should be called once at startup, not per
// render.
func Detect() Protocol {
	if rasterm.IsKittyCapable() {
		return ProtocolKitty
	}
	if lipgloss.ColorProfile() == termenv.TrueColor {
		return ProtocolBlocks
	}
	return ProtocolNone
}
