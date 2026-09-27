package tui

import (
	"fmt"
	"image/color"
	"kari/internal/model"
	"math"
	"strconv"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// Semantic color tokens using AdaptiveColor for dark/light terminal support.
var (
	ColorDim = lipgloss.AdaptiveColor{
		Dark:  "#7D8590",
		Light: "#656D76",
	}
	ColorOk = lipgloss.AdaptiveColor{
		Dark:  "#7DCFAD",
		Light: "#1A7F5A",
	}
	ColorErr = lipgloss.AdaptiveColor{
		Dark:  "#F7768E",
		Light: "#CF222E",
	}
)

// ModeTheme defines the visual identity tokens for a single content mode.
type ModeTheme struct {
	Accent    lipgloss.AdaptiveColor
	AccentDim lipgloss.AdaptiveColor
}

// Dark base mode palette from spec §3.
// anime #F28FAD, cartoon #7DCFAD, live #FF6B4A, manga #BB9AF7, movies #E5B567, tv #7AA2F7, jellyfin #5CCFE6.
// Light variants derived by lowering lightness to maintain >= 4.5:1 contrast on light base (#FAFAFA).
var modeThemes = map[model.Kind]ModeTheme{
	model.KindAnime: {
		Accent: lipgloss.AdaptiveColor{
			Dark:  "#F28FAD",
			Light: "#9B5C6F",
		},
		AccentDim: lipgloss.AdaptiveColor{
			Dark:  "#A86378",
			Light: "#C48A9A",
		},
	},
	model.KindCartoon: {
		Accent: lipgloss.AdaptiveColor{
			Dark:  "#7DCFAD",
			Light: "#487864",
		},
		AccentDim: lipgloss.AdaptiveColor{
			Dark:  "#528872",
			Light: "#82AB9B",
		},
	},
	model.KindLive: {
		Accent: lipgloss.AdaptiveColor{
			Dark:  "#FF6B4A",
			Light: "#B54C35",
		},
		AccentDim: lipgloss.AdaptiveColor{
			Dark:  "#A84731",
			Light: "#D97B66",
		},
	},
	model.KindManga: {
		Accent: lipgloss.AdaptiveColor{
			Dark:  "#BB9AF7",
			Light: "#7A64A1",
		},
		AccentDim: lipgloss.AdaptiveColor{
			Dark:  "#7D67A6",
			Light: "#A996CB",
		},
	},
	model.KindMovie: {
		Accent: lipgloss.AdaptiveColor{
			Dark:  "#E5B567",
			Light: "#876B3D",
		},
		AccentDim: lipgloss.AdaptiveColor{
			Dark:  "#967744",
			Light: "#BD9E6B",
		},
	},
	model.KindTV: {
		Accent: lipgloss.AdaptiveColor{
			Dark:  "#7AA2F7",
			Light: "#526DA5",
		},
		AccentDim: lipgloss.AdaptiveColor{
			Dark:  "#516BA3",
			Light: "#89A4DB",
		},
	},
	model.KindJellyfin: {
		Accent: lipgloss.AdaptiveColor{
			Dark:  "#5CCFE6",
			Light: "#357885",
		},
		AccentDim: lipgloss.AdaptiveColor{
			Dark:  "#3D8896",
			Light: "#72B5C4",
		},
	},
}

// Preset fixed accent overrides.
var FixedAccents = []struct {
	Name  string
	Color lipgloss.AdaptiveColor
}{
	{
		Name: "Auto",
		Color: lipgloss.AdaptiveColor{
			Dark:  "#F28FAD",
			Light: "#9B5C6F",
		},
	},
	{
		Name: "Purple",
		Color: lipgloss.AdaptiveColor{
			Dark:  "#BB9AF7",
			Light: "#7A64A1",
		},
	},
	{
		Name: "Blue",
		Color: lipgloss.AdaptiveColor{
			Dark:  "#7AA2F7",
			Light: "#526DA5",
		},
	},
	{
		Name: "Cyan",
		Color: lipgloss.AdaptiveColor{
			Dark:  "#70C0BA",
			Light: "#3B7C77",
		},
	},
	{
		Name: "Teal",
		Color: lipgloss.AdaptiveColor{
			Dark:  "#5CCFE6",
			Light: "#357885",
		},
	},
	{
		Name: "Green",
		Color: lipgloss.AdaptiveColor{
			Dark:  "#7DCFAD",
			Light: "#487864",
		},
	},
	{
		Name: "Yellow",
		Color: lipgloss.AdaptiveColor{
			Dark:  "#E5C07B",
			Light: "#8C6D2B",
		},
	},
	{
		Name: "Orange",
		Color: lipgloss.AdaptiveColor{
			Dark:  "#FF6B4A",
			Light: "#B54C35",
		},
	},
	{
		Name: "Pink",
		Color: lipgloss.AdaptiveColor{
			Dark:  "#F28FAD",
			Light: "#9B5C6F",
		},
	},
	{
		Name: "Magenta",
		Color: lipgloss.AdaptiveColor{
			Dark:  "#C678DD",
			Light: "#843B99",
		},
	},
	{
		Name: "Gold",
		Color: lipgloss.AdaptiveColor{
			Dark:  "#E5B567",
			Light: "#876B3D",
		},
	},
}

// ThemeFor returns the full ModeTheme (Accent, AccentDim) for the given
// content kind and user override setting.
func ThemeFor(kind model.Kind, overrideSetting string) ModeTheme {
	setting := strings.TrimSpace(overrideSetting)
	if setting == "" || strings.EqualFold(setting, "auto") {
		if t, ok := modeThemes[kind]; ok {
			return t
		}
		return modeThemes[model.KindAnime]
	}

	for _, p := range FixedAccents {
		if strings.EqualFold(p.Name, setting) {
			return ModeTheme{
				Accent:    p.Color,
				AccentDim: p.Color,
			}
		}
	}

	// Custom hex
	if strings.HasPrefix(setting, "#") || len(setting) == 6 {
		hex := setting
		if !strings.HasPrefix(hex, "#") {
			hex = "#" + hex
		}
		adapt := lipgloss.AdaptiveColor{Dark: hex, Light: hex}
		return ModeTheme{
			Accent:    adapt,
			AccentDim: adapt,
		}
	}

	if t, ok := modeThemes[kind]; ok {
		return t
	}
	return modeThemes[model.KindAnime]
}

// ResolveAccent computes the active accent color token given the current
// kind and override setting. Unknown kinds fall back to the anime accent.
func ResolveAccent(kind model.Kind, overrideSetting string) lipgloss.AdaptiveColor {
	return ThemeFor(kind, overrideSetting).Accent
}

// HexToRGBA parses a 6-digit hex color "#RRGGBB" or "RRGGBB" into color.RGBA.
func HexToRGBA(hex string) color.RGBA {
	hex = strings.TrimPrefix(strings.TrimSpace(hex), "#")
	if len(hex) != 6 {
		return color.RGBA{A: 255}
	}
	r, err1 := strconv.ParseUint(hex[0:2], 16, 8)
	g, err2 := strconv.ParseUint(hex[2:4], 16, 8)
	b, err3 := strconv.ParseUint(hex[4:6], 16, 8)
	if err1 != nil || err2 != nil || err3 != nil {
		return color.RGBA{A: 255}
	}
	return color.RGBA{R: uint8(r), G: uint8(g), B: uint8(b), A: 255}
}

// RGBToHex formats a color.RGBA into a "#RRGGBB" hex string.
func RGBToHex(c color.RGBA) string {
	return fmt.Sprintf("#%02X%02X%02X", c.R, c.G, c.B)
}

// RelativeLuminance computes the relative luminance (0..1) of an sRGB color per WCAG 2.1.
func RelativeLuminance(c color.RGBA) float64 {
	toLinear := func(val uint8) float64 {
		v := float64(val) / 255.0
		if v <= 0.04045 {
			return v / 12.92
		}
		return math.Pow((v+0.055)/1.055, 2.4)
	}
	return 0.2126*toLinear(c.R) + 0.7152*toLinear(c.G) + 0.0722*toLinear(c.B)
}

// ContrastRatio computes the WCAG contrast ratio (1..21) between two colors.
func ContrastRatio(c1, c2 color.RGBA) float64 {
	l1 := RelativeLuminance(c1)
	l2 := RelativeLuminance(c2)
	bright := math.Max(l1, l2)
	dark := math.Min(l1, l2)
	return (bright + 0.05) / (dark + 0.05)
}

// MixRGB blends two colors linearly by factor a (0..1).
func MixRGB(c1, c2 color.RGBA, a float64) color.RGBA {
	a = math.Max(0.0, math.Min(1.0, a))
	r := float64(c1.R)*(1.0-a) + float64(c2.R)*a
	g := float64(c1.G)*(1.0-a) + float64(c2.G)*a
	b := float64(c1.B)*(1.0-a) + float64(c2.B)*a
	return color.RGBA{
		R: uint8(math.Round(r)),
		G: uint8(math.Round(g)),
		B: uint8(math.Round(b)),
		A: 255,
	}
}

// SmoothMixRGB blends two colors using gamma-corrected perceptual interpolation
// and an S-curve easing profile, preserving luminance across theme crossfades.
func SmoothMixRGB(c1, c2 color.RGBA, a float64) color.RGBA {
	a = math.Max(0.0, math.Min(1.0, a))
	// Smoothstep cubic easing: 3a^2 - 2a^3
	t := a * a * (3.0 - 2.0*a)

	r1, g1, b1 := float64(c1.R)*float64(c1.R), float64(c1.G)*float64(c1.G), float64(c1.B)*float64(c1.B)
	r2, g2, b2 := float64(c2.R)*float64(c2.R), float64(c2.G)*float64(c2.G), float64(c2.B)*float64(c2.B)

	r := math.Sqrt(r1*(1.0-t) + r2*t)
	g := math.Sqrt(g1*(1.0-t) + g2*t)
	b := math.Sqrt(b1*(1.0-t) + b2*t)

	return color.RGBA{
		R: uint8(math.Round(math.Min(255, r))),
		G: uint8(math.Round(math.Min(255, g))),
		B: uint8(math.Round(math.Min(255, b))),
		A: 255,
	}
}
