package tui

import (
	"testing"

	"kari/internal/model"
	"kari/internal/provider"
)

func Test16StepCrossfadeTheme(t *testing.T) {
	m := &modelImpl{
		appMode:         provider.ModeAnime,
		activeView:      viewSearch,
		crossfadeActive: true,
		crossfadeStep:   8, // Midpoint
		crossfadeFrom:   ThemeFor(model.KindAnime, "auto"),
		crossfadeTo:     ThemeFor(model.KindTV, "auto"),
	}

	thm := m.currentModeTheme()
	if thm.Accent.Dark == "" || thm.AccentDim.Dark == "" {
		t.Fatalf("crossfaded theme must have non-empty accent tokens")
	}

	// At step 8 of 16, accent must not be identical to either end
	fromDark := m.crossfadeFrom.Accent.Dark
	toDark := m.crossfadeTo.Accent.Dark
	if thm.Accent.Dark == fromDark || thm.Accent.Dark == toDark {
		t.Errorf("midpoint accent %s should differ from both endpoints %s and %s", thm.Accent.Dark, fromDark, toDark)
	}
}
