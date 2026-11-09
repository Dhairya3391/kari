package tui

import (
	"testing"

	"github.com/charmbracelet/lipgloss"
)

func TestTruncateUsesTerminalCellWidth(t *testing.T) {
	got := truncate("界界界界", 5)
	if width := lipgloss.Width(got); width > 5 {
		t.Fatalf("truncate width = %d, want at most 5: %q", width, got)
	}
}
