// Package termimg detects terminal image-rendering capability and renders
// images through the best available protocol: Kitty graphics, iTerm2 inline
// images, Sixel, or quadrant-block truecolor ANSI art as a final fallback.
// See render.go for how every protocol composes with Bubble Tea's
// line-based redraw model, and kitty.go/sixel.go/iterm.go for the
// protocol-specific tricks involved.
package termimg

import (
	"os"
	"strings"

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
	// ProtocolSixel renders images via DECSIXEL graphics.
	ProtocolSixel
	// ProtocolIterm renders images via the iTerm2 inline-images protocol
	// (also understood by WezTerm, Rio, Mintty, mlterm and iTerm2 itself).
	ProtocolIterm
	// ProtocolKitty renders images via the Kitty graphics protocol.
	ProtocolKitty
)

// String returns the stable lowercase name of the protocol, used in logs
// and in the KARI_IMG_PROTOCOL override.
func (p Protocol) String() string {
	switch p {
	case ProtocolKitty:
		return "kitty"
	case ProtocolIterm:
		return "iterm"
	case ProtocolSixel:
		return "sixel"
	case ProtocolBlocks:
		return "blocks"
	default:
		return "none"
	}
}

// NeedsCleanup reports whether the protocol leaves persistent placements
// behind that must be explicitly deleted when the image stops being shown.
// Only Kitty behaves this way; Sixel/iTerm2/blocks output is ordinary
// scrolling cell content that the next frame overwrites on its own.
func (p Protocol) NeedsCleanup() bool {
	return p == ProtocolKitty
}

// Cleanup returns the escape sequence that clears a previous placement of
// the given slot image id: a single delete-by-id. Deliberately
// transmit-free — emitting image data for cleanup made terminals answer
// EINVAL, and those error responses land on stdin as typed garbage in
// the search box. It returns "" for protocols that need no cleanup, so
// callers can invoke it unconditionally.
func (p Protocol) Cleanup(imageID uint32) string {
	if p == ProtocolKitty {
		return DeleteKitty(imageID)
	}
	return ""
}

// DeleteAll returns the escape sequence that clears every visible image
// placement. It returns "" for protocols that need no cleanup.
func (p Protocol) DeleteAll() string {
	if p == ProtocolKitty {
		return DeleteAllKitty()
	}
	return ""
}

// protocolOverrideEnv forces Detect to return a specific protocol, for
// testing and for users whose terminal misreports its capabilities
// (e.g. an xterm build without Sixel, or Sixel behind tmux).
const protocolOverrideEnv = "KARI_IMG_PROTOCOL"

// Detect probes the current terminal once and returns the best available
// image rendering protocol. It should be called once at startup, not per
// render.
//
// Detection is deliberately non-blocking: it only consults environment
// variables (via rasterm) and the lipgloss color profile. The rigorous
// Sixel probe (a DA1 query/response round-trip, see
// rasterm.RequestTermAttributes) takes ~60ms and puts stdin into raw mode,
// so it must never run on the startup path — Sixel is instead enabled on
// terminal types with reliable built-in support (mlterm, foot). Anyone
// misdetected in either direction can force the right answer with
// KARI_IMG_PROTOCOL=kitty|iterm|sixel|blocks|none.
func Detect() Protocol {
	if override, ok := parseOverride(os.Getenv(protocolOverrideEnv)); ok {
		return override
	}
	// Kitty first: WezTerm and Ghostty report Kitty capability and
	// render Kitty transmits natively — verified live on Ghostty,
	// where the iTerm2 inline path shows nothing at all. Slot cleanup
	// goes through explicit delete-by-id sequences (see kitty.go).
	if rasterm.IsKittyCapable() {
		return ProtocolKitty
	}
	if rasterm.IsItermCapable() {
		return ProtocolIterm
	}
	if sixelEnvHint() {
		return ProtocolSixel
	}
	if lipgloss.ColorProfile() == termenv.TrueColor {
		return ProtocolBlocks
	}
	return ProtocolNone
}

// parseOverride maps a KARI_IMG_PROTOCOL value to its protocol.
func parseOverride(raw string) (Protocol, bool) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "kitty":
		return ProtocolKitty, true
	case "iterm", "iterm2":
		return ProtocolIterm, true
	case "sixel":
		return ProtocolSixel, true
	case "blocks", "halfblocks", "quadrants", "ansi":
		return ProtocolBlocks, true
	case "none", "off", "false":
		return ProtocolNone, true
	default:
		return ProtocolNone, false
	}
}

// sixelEnvHint reports whether the environment looks like a Sixel-capable
// terminal that neither the Kitty nor the iTerm2 check already claimed.
// Only terminals with reliable built-in Sixel qualify (mlterm, foot).
// Plain xterm is deliberately excluded: stock builds vary, macOS
// Terminal.app sets xterm-256color with no Sixel at all, and a false
// Sixel claim prints raw sixel bytes as garbage — while quadrant blocks
// always render. Real sixel-xterm users force KARI_IMG_PROTOCOL=sixel.
func sixelEnvHint() bool {
	term := strings.ToLower(strings.TrimSpace(os.Getenv("TERM")))
	for _, hint := range []string{"mlterm", "foot", "sixel"} {
		if strings.Contains(term, hint) {
			return true
		}
	}
	return false
}
