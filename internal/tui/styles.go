package tui

import (
	"regexp"
	"strings"

	"github.com/charmbracelet/bubbles/progress"
	"github.com/charmbracelet/lipgloss"

	"kari/internal/provider"
)

var (
	colorSurface = lipgloss.Color("#161616")
	colorBorder  = lipgloss.Color("#393939")
	colorText    = lipgloss.Color("#f2f4f8")
	colorMuted   = lipgloss.Color("#8d8d8d")

	colorPrimary = lipgloss.Color("#be95ff")
	colorInfo    = lipgloss.Color("#33b1ff")
	colorSuccess = lipgloss.Color("#42be65")
	colorWarn    = lipgloss.Color("#ffcc00")
	colorError   = lipgloss.Color("#ff5555")

	mutedStyle = lipgloss.NewStyle().Foreground(colorMuted)
	textStyle  = lipgloss.NewStyle().Foreground(colorText)

	sectionTitleStyle = lipgloss.NewStyle().
				Foreground(colorPrimary).
				Bold(true)

	keyStyle = lipgloss.NewStyle().
			Foreground(colorSurface).
			Background(colorMuted).
			Padding(0, 1)

	cardStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(colorBorder).
			Padding(1, 2)

	infoStyle = lipgloss.NewStyle().Foreground(colorInfo)

	// Pre-cached badge styles for performance in hot render paths
	badgeBase = lipgloss.NewStyle().
			Foreground(colorSurface).
			Bold(true).
			Padding(0, 1)

	modeBadgeAnime    = badgeBase.Background(lipgloss.Color("#be95ff"))
	modeBadgeCartoon  = badgeBase.Background(lipgloss.Color("#42be65"))
	modeBadgeMovies   = badgeBase.Background(lipgloss.Color("#33b1ff"))
	modeBadgeTV       = badgeBase.Background(lipgloss.Color("#08bdba"))
	modeBadgeJellyfin = badgeBase.Background(lipgloss.Color("#ee5396"))
	modeBadgeDefault  = badgeBase.Background(colorMuted)
	preparingBadge    = badgeBase.Background(colorInfo).Render("PREPARING")
	fillerBadgeStr    = badgeBase.MarginLeft(1).Background(colorWarn).Render("FILLER")
)

// accentPresets is the curated list of accent colors offered in Settings —
// all Carbon-family hues already used elsewhere in the app (mode badges,
// info/success colors), so picking any of them keeps the palette cohesive
// rather than clashing with the rest of the UI.
var accentPresets = []struct{ name, hex string }{
	{"Purple", "#be95ff"}, // default
	{"Blue", "#33b1ff"},
	{"Green", "#42be65"},
	{"Pink", "#ee5396"},
	{"Teal", "#08bdba"},
	{"Orange", "#ff832b"},
}

// SetAccentColor repoints the app's accent color. colorPrimary itself is a
// plain var re-read at call time everywhere else it's used (list delegates,
// every view_*.go render function), so those pick up the change on their
// own. sectionTitleStyle and modeBadgeAnime are the only two styles that
// cache colorPrimary at Go package-init time (before any settings are
// loaded) rather than at render time, so they're rebuilt explicitly here.
func SetAccentColor(hex string) {
	if hex == "" {
		return
	}
	colorPrimary = lipgloss.Color(hex)
	sectionTitleStyle = lipgloss.NewStyle().Foreground(colorPrimary).Bold(true)
	modeBadgeAnime = badgeBase.Background(colorPrimary)
}

// newDownloadBar builds a progress bar filled with the current accent
// color. Called at startup and again whenever the accent color changes, so
// an in-progress download's bar isn't left showing a stale color.
func newDownloadBar() progress.Model {
	return progress.New(progress.WithSolidFill(string(colorPrimary)))
}

var hexColorRe = regexp.MustCompile(`^[0-9a-fA-F]{6}$`)

// normalizeHexColor validates a user-typed or persisted color string (with
// or without a leading '#') and returns it in canonical "#rrggbb" form. ok
// is false for anything that isn't exactly 6 hex digits — used both for
// the custom-accent text input and defensively when loading settings.json,
// in case it was hand-edited to something invalid.
func normalizeHexColor(s string) (string, bool) {
	s = strings.TrimPrefix(strings.TrimSpace(s), "#")
	if !hexColorRe.MatchString(s) {
		return "", false
	}
	return "#" + strings.ToLower(s), true
}

func renderBadge(mode string) string {
	label := strings.ToUpper(mode)
	switch mode {
	case provider.MediaTypeAnime:
		return modeBadgeAnime.Render(label)
	case provider.MediaTypeCartoon:
		return modeBadgeCartoon.Render(label)
	case provider.MediaTypeMovie, string(provider.ModeMovies):
		return modeBadgeMovies.Render(label)
	case provider.MediaTypeTV:
		return modeBadgeTV.Render(label)
	case string(provider.ModeJellyfin):
		return modeBadgeJellyfin.Render(label)
	default:
		return modeBadgeDefault.Render(label)
	}
}
