package tui

import (
	"regexp"
	"strings"

	"github.com/charmbracelet/bubbles/progress"
	"github.com/charmbracelet/lipgloss"
)

var (
	colorText    = lipgloss.Color("#f2f4f8")
	colorMuted   = lipgloss.Color("#8d8d8d")
	colorPrimary = lipgloss.Color("#be95ff")
)
var accentPresets = []struct{ name, hex string }{
	{"Auto (per mode)", "auto"},
	{"Purple", "#BB9AF7"},
	{"Blue", "#7AA2F7"},
	{"Cyan", "#70C0BA"},
	{"Teal", "#5CCFE6"},
	{"Green", "#7DCFAD"},
	{"Yellow", "#E5C07B"},
	{"Orange", "#FF6B4A"},
	{"Pink", "#F28FAD"},
	{"Magenta", "#C678DD"},
	{"Gold", "#E5B567"},
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
