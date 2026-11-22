package tui

import (
	"strings"
	"testing"
)

func TestClampHelpScrollReachesEndOfContent(t *testing.T) {
	m := &modelImpl{activeView: viewSearch, width: 100}

	unscrolled := m.renderHelpOverlayContentAtScroll(m.width, m.currentAccent(), 0)
	contentLines := max(0, strings.Count(unscrolled, "\n")+1-helpOverlayFrameLines)
	if contentLines < 2 {
		t.Fatalf("help content only has %d lines", contentLines)
	}

	// A page-down overshoot must clamp to the last content line. Deriving the
	// bound from the already-scrolled box would shrink it with the scroll and
	// cap it near half the content.
	m.helpScroll = contentLines + 10
	m.clampHelpScroll()
	if m.helpScroll != contentLines {
		t.Fatalf("overshoot clamped to %d, want %d", m.helpScroll, contentLines)
	}

	// Single-line steps must reach the same end.
	m.helpScroll = 0
	for i := 0; i < contentLines+5; i++ {
		m.helpScroll++
		m.clampHelpScroll()
	}
	if m.helpScroll != contentLines {
		t.Fatalf("stepped scroll reached %d, want %d", m.helpScroll, contentLines)
	}
}
