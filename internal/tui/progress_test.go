package tui

// The fill must stay proportional: 0m/2h24m rows showed a lit cell because
// any nonzero ratio forced one filled cell.

import (
	"strings"
	"testing"
)

func filledCells(t *testing.T, ratio float64, total int) int {
	t.Helper()
	bar := RenderProgressBar(ratio, total, ResolveAccent(0, "Auto"))
	return strings.Count(bar, "▰")
}

func TestRenderProgressBarAccuracy(t *testing.T) {
	accent := ResolveAccent(0, "Auto")
	if got := filledCells(t, 0, 10); got != 0 {
		t.Errorf("ratio 0: %d filled, want 0", got)
	}
	// 30s of a 2h24m movie: rounds to "0m" in text, must not light a cell.
	if got := filledCells(t, 30.0/8640.0, 10); got != 0 {
		t.Errorf("sub-minute progress: %d filled, want 0", got)
	}
	if got := filledCells(t, 0.06, 10); got != 1 {
		t.Errorf("ratio 0.06: %d filled, want 1", got)
	}
	if got := filledCells(t, 0.5, 10); got != 5 {
		t.Errorf("ratio 0.5: %d filled, want 5", got)
	}
	if got := filledCells(t, 1, 10); got != 10 {
		t.Errorf("ratio 1: %d filled, want 10", got)
	}
	if got := filledCells(t, 1.5, 10); got != 10 {
		t.Errorf("ratio >1 must clamp: %d filled, want 10", got)
	}
	if got := filledCells(t, -0.5, 10); got != 0 {
		t.Errorf("negative ratio: %d filled, want 0", got)
	}
	if out := RenderProgressBar(0.5, 10, accent); strings.Count(out, "▱") != 5 {
		t.Errorf("empty cells wrong: %q", out)
	}
	if out := RenderProgressBar(0.5, 0, accent); out != "" {
		t.Errorf("zero width must be empty: %q", out)
	}
}
