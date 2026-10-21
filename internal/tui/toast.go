package tui

import (
	"time"

	"github.com/charmbracelet/lipgloss"
)

// ToastType determines the icon and coloring of a toast message.
type ToastType int

const (
	ToastInfo ToastType = iota
	ToastSuccess
	ToastError
)

// Toast represents an active transient notification.
type Toast struct {
	Message   string
	Type      ToastType
	CreatedAt time.Time
	ExpiresAt time.Time
}

// NewToast creates a new Toast message expiring in duration (default ~3s).
func NewToast(msg string, ttype ToastType, duration time.Duration) *Toast {
	if duration <= 0 {
		duration = 3 * time.Second
	}
	now := time.Now()
	return &Toast{
		Message:   msg,
		Type:      ttype,
		CreatedAt: now,
		ExpiresAt: now.Add(duration),
	}
}

// IsExpired checks whether the toast has exceeded its lifetime.
func (t *Toast) IsExpired(now time.Time) bool {
	if t == nil {
		return true
	}
	return now.After(t.ExpiresAt)
}

// Render renders the toast message row.
func (t *Toast) Render(accent lipgloss.AdaptiveColor) string {
	return t.RenderWithTime(accent, time.Now())
}

// RenderWithTime renders the toast message row with gentle soft fade-out dissolution
// during the final 350ms of its lifetime.
func (t *Toast) RenderWithTime(accent lipgloss.AdaptiveColor, now time.Time) string {
	if t == nil || t.Message == "" || t.IsExpired(now) {
		return ""
	}

	st := NewStyles(accent)

	var icon string
	var iconColor lipgloss.AdaptiveColor
	switch t.Type {
	case ToastSuccess:
		icon = "✓ "
		iconColor = ColorOk
	case ToastError:
		icon = "✗ "
		iconColor = ColorErr
	default:
		icon = "• "
		iconColor = accent
	}

	remaining := t.ExpiresAt.Sub(now)
	const fadeDuration = 350 * time.Millisecond

	if remaining < fadeDuration {
		progress := float64(remaining) / float64(fadeDuration)
		if progress < 0 {
			progress = 0
		} else if progress > 1 {
			progress = 1
		}

		// Interpolate icon color from iconColor towards ColorDim
		dimDark := HexToRGBA(ColorDim.Dark)
		dimLight := HexToRGBA(ColorDim.Light)
		icoDark := HexToRGBA(iconColor.Dark)
		icoLight := HexToRGBA(iconColor.Light)

		fadedIcoDark := SmoothMixRGB(dimDark, icoDark, progress)
		fadedIcoLight := SmoothMixRGB(dimLight, icoLight, progress)

		fadedIconStyle := lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{
			Dark:  RGBToHex(fadedIcoDark),
			Light: RGBToHex(fadedIcoLight),
		})

		// Interpolate message color from standard text towards ColorDim
		fgDark := HexToRGBA("#FFFFFF")
		fgLight := HexToRGBA("#1F2328")

		fadedTextDark := SmoothMixRGB(dimDark, fgDark, progress)
		fadedTextLight := SmoothMixRGB(dimLight, fgLight, progress)

		fadedTextStyle := lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{
			Dark:  RGBToHex(fadedTextDark),
			Light: RGBToHex(fadedTextLight),
		})

		return fadedIconStyle.Render(icon) + fadedTextStyle.Render(t.Message)
	}

	switch t.Type {
	case ToastSuccess:
		return st.Ok.Render("✓ ") + t.Message
	case ToastError:
		return st.Err.Render("✗ ") + t.Message
	default:
		return st.Dim.Render("• ") + t.Message
	}
}
