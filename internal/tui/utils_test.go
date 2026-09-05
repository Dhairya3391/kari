package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
)

func TestScrollLinesBoundsOutput(t *testing.T) {
	content := "one\ntwo\nthree\nfour\nfive"
	got, offset := scrollLines(content, 99, 3, "ctrl+u/d scroll")

	if offset != 3 {
		t.Fatalf("offset = %d, want 3", offset)
	}
	if height := strings.Count(got, "\n") + 1; height != 3 {
		t.Fatalf("output height = %d, want 3", height)
	}
	if !strings.Contains(got, "four\nfive") || !strings.Contains(got, "4–5 of 5") {
		t.Fatalf("output = %q, want final content and range indicator", got)
	}
}

func TestFitFooterBindingsDoesNotWrap(t *testing.T) {
	parts := []string{"one", "two", "three"}
	got := fitFooterBindings(parts, 10)

	if lipgloss.Width(got) > 10 {
		t.Fatalf("footer width = %d, want at most 10", lipgloss.Width(got))
	}
	if !strings.Contains(got, "…") {
		t.Fatalf("footer = %q, want overflow indicator", got)
	}
}

func TestMoveSettingsStopsAtBounds(t *testing.T) {
	m := modelImpl{settingsIndex: 0}
	m.moveSettings(-1)
	if m.settingsIndex != 0 {
		t.Fatalf("top settings index = %d, want 0", m.settingsIndex)
	}

	m.settingsIndex = settingsLastIndex
	m.moveSettings(1)
	if m.settingsIndex != settingsLastIndex {
		t.Fatalf("bottom settings index = %d, want %d", m.settingsIndex, settingsLastIndex)
	}
}
