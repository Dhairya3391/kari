package tui

import "github.com/charmbracelet/lipgloss"

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

	modeBadgeAnime   = badgeBase.Background(lipgloss.Color("#be95ff"))
	modeBadgeCartoon = badgeBase.Background(lipgloss.Color("#42be65"))
	modeBadgeMovies  = badgeBase.Background(lipgloss.Color("#33b1ff"))
	modeBadgeTV      = badgeBase.Background(lipgloss.Color("#08bdba"))
	modeBadgeDefault = badgeBase.Background(colorMuted)
	preparingBadge   = badgeBase.Background(colorInfo).Render("PREPARING")
	fillerBadgeStr   = badgeBase.MarginLeft(1).Background(colorWarn).Render("FILLER")
)

func renderBadge(mode string) string {
	switch mode {
	case "anime":
		return modeBadgeAnime.Render(mode)
	case "cartoon":
		return modeBadgeCartoon.Render(mode)
	case "movies":
		return modeBadgeMovies.Render(mode)
	case "tv":
		return modeBadgeTV.Render(mode)
	default:
		return modeBadgeDefault.Render(mode)
	}
}
