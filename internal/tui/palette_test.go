package tui

import (
	"testing"

	"kari/internal/model"
)

var allTestModes = []model.Kind{
	model.KindAnime,
	model.KindCartoon,
	model.KindLive,
	model.KindManga,
	model.KindMovie,
	model.KindTV,
	model.KindJellyfin,
}

func TestLegibilityGate(t *testing.T) {
	darkBase := HexToRGBA("#0B0D10")
	lightBase := HexToRGBA("#FAFAFA")
	fgDark := HexToRGBA("#FFFFFF")
	fgLight := HexToRGBA("#1F2328")
	dimDark := HexToRGBA(ColorDim.Dark)
	dimLight := HexToRGBA(ColorDim.Light)

	for _, kind := range allTestModes {
		thm := ThemeFor(kind, "Auto")

		// 1. Dark base check.
		accentDark := HexToRGBA(thm.Accent.Dark)

		fgDarkCr := ContrastRatio(fgDark, darkBase)
		accentDarkCr := ContrastRatio(accentDark, darkBase)
		dimDarkCr := ContrastRatio(dimDark, darkBase)

		if fgDarkCr < 7.0 {
			t.Errorf("mode %s (dark): fg contrast %.2f < 7.0:1", kind.Key(), fgDarkCr)
		}
		if accentDarkCr < 4.5 {
			t.Errorf("mode %s (dark): accent contrast %.2f < 4.5:1", kind.Key(), accentDarkCr)
		}
		if dimDarkCr < 3.5 {
			t.Errorf("mode %s (dark): dim contrast %.2f < 3.5:1", kind.Key(), dimDarkCr)
		}

		// 2. Light base check.
		accentLight := HexToRGBA(thm.Accent.Light)

		fgLightCr := ContrastRatio(fgLight, lightBase)
		accentLightCr := ContrastRatio(accentLight, lightBase)
		dimLightCr := ContrastRatio(dimLight, lightBase)

		if fgLightCr < 7.0 {
			t.Errorf("mode %s (light): fg contrast %.2f < 7.0:1", kind.Key(), fgLightCr)
		}
		if accentLightCr < 4.5 {
			t.Errorf("mode %s (light): accent contrast %.2f < 4.5:1", kind.Key(), accentLightCr)
		}
		if dimLightCr < 3.5 {
			t.Errorf("mode %s (light): dim contrast %.2f < 3.5:1", kind.Key(), dimLightCr)
		}
	}
}

func TestSmootherstep(t *testing.T) {
	if s := Smootherstep(0.0); s != 0.0 {
		t.Errorf("Smootherstep(0) = %f, want 0", s)
	}
	if s := Smootherstep(1.0); s != 1.0 {
		t.Errorf("Smootherstep(1) = %f, want 1", s)
	}
	if s := Smootherstep(0.5); s != 0.5 {
		t.Errorf("Smootherstep(0.5) = %f, want 0.5", s)
	}
	if s := Smootherstep(-0.5); s != 0.0 {
		t.Errorf("Smootherstep(-0.5) = %f, want 0", s)
	}
	if s := Smootherstep(1.5); s != 1.0 {
		t.Errorf("Smootherstep(1.5) = %f, want 1", s)
	}
}

func TestThemeForOverride(t *testing.T) {
	// Fixed preset override
	thm := ThemeFor(model.KindAnime, "Green")
	if thm.Accent.Dark != "#7DCFAD" {
		t.Errorf("got accent %s, want #7DCFAD", thm.Accent.Dark)
	}

	// Custom hex override
	thmCustom := ThemeFor(model.KindAnime, "#123456")
	if thmCustom.Accent.Dark != "#123456" {
		t.Errorf("got custom accent %s, want #123456", thmCustom.Accent.Dark)
	}
}
