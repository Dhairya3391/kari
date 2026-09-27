package tui

// Season-strip windowing: long runs collapse around the active season
// and never exceed the given width.

import (
	"kari/internal/model"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
)

func stripLineWidths(t *testing.T, out string) []int {
	t.Helper()
	var widths []int
	for _, line := range strings.Split(out, "\n") {
		widths = append(widths, lipgloss.Width(line))
	}
	return widths
}

// Short runs render fully with no markers, unchanged.
func TestRenderSeasonTabsShortUnchanged(t *testing.T) {
	accent := ResolveAccent(model.KindTV, "Auto")
	out := RenderSeasonTabs(5, 0, accent, 70)
	if !strings.Contains(out, "season") {
		t.Errorf("missing prefix, got:\n%s", out)
	}
	for _, n := range []string{"1", "2", "3", "4", "5"} {
		if !strings.Contains(out, n) {
			t.Errorf("missing tab %s, got:\n%s", n, out)
		}
	}
	if strings.Contains(out, "‹") || strings.Contains(out, "›") {
		t.Errorf("no markers when everything fits, got:\n%s", out)
	}
}

// Fifteen seasons at list width collapse around the active tab with
// overflow markers, and no line exceeds the width.
func TestRenderSeasonTabsWindowed(t *testing.T) {
	accent := ResolveAccent(model.KindTV, "Auto")
	out := RenderSeasonTabs(15, 14, accent, 70)
	if !strings.Contains(out, "15") {
		t.Errorf("active tab must stay visible, got:\n%s", out)
	}
	if !strings.Contains(out, "‹") {
		t.Errorf("hidden left seasons need a marker, got:\n%s", out)
	}
	if strings.Contains(out, "›") {
		t.Errorf("nothing hidden on the right, got:\n%s", out)
	}
	for i, w := range stripLineWidths(t, out) {
		if w > 70 {
			t.Errorf("line %d width %d exceeds 70", i, w)
		}
	}
}

// Active tab in the middle keeps both markers and stays visible.
func TestRenderSeasonTabsWindowedMiddle(t *testing.T) {
	accent := ResolveAccent(model.KindTV, "Auto")
	out := RenderSeasonTabs(15, 7, accent, 60)
	if !strings.Contains(out, "8") {
		t.Errorf("active tab must stay visible, got:\n%s", out)
	}
	if !strings.Contains(out, "‹") || !strings.Contains(out, "›") {
		t.Errorf("both markers expected, got:\n%s", out)
	}
	for i, w := range stripLineWidths(t, out) {
		if w > 60 {
			t.Errorf("line %d width %d exceeds 60", i, w)
		}
	}
}
