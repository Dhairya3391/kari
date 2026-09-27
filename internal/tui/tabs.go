package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// RenderTabs produces a two-line horizontal tab strip:
// Line 1: Tab labels (active is UPPERCASE + accent bold, others are lowercase + dim).
// Line 2: Underline directly beneath the active tab only.
func RenderTabs(tabs []string, activeIndex int, accent lipgloss.AdaptiveColor) string {
	if len(tabs) == 0 {
		return ""
	}

	st := NewStyles(accent)

	var labelParts []string
	var underlineParts []string

	for i, tab := range tabs {
		raw := strings.TrimSpace(tab)
		if i == activeIndex {
			upper := strings.ToUpper(raw)
			labelParts = append(labelParts, st.ActiveTab.Render(upper))
			underlineParts = append(underlineParts, st.Underline.Render(strings.Repeat("─", len(upper))))
		} else {
			lower := strings.ToLower(raw)
			labelParts = append(labelParts, st.InactiveTab.Render(lower))
			underlineParts = append(underlineParts, strings.Repeat(" ", len(lower)))
		}
	}

	// 5-space separation between tabs matching mockup
	line1 := strings.Join(labelParts, "     ")
	line2 := strings.Join(underlineParts, "     ")

	return line1 + "\n" + line2
}

// RenderSeasonTabs produces the season selector strip, windowed to
// maxWidth (0 = unbounded):
// Line 1: season   1     2     3     4     5
// Line 2:          ─
// Long runs collapse to a window around the active season with ‹ ›
// overflow markers, so a 15-season strip never bleeds past its column
// into the inspector pane.
func RenderSeasonTabs(seasonCount int, activeSeasonIndex int, accent lipgloss.AdaptiveColor, maxWidth int) string {
	if seasonCount <= 1 {
		return ""
	}
	if activeSeasonIndex < 0 {
		activeSeasonIndex = 0
	}
	if activeSeasonIndex >= seasonCount {
		activeSeasonIndex = seasonCount - 1
	}

	st := NewStyles(accent)

	seasonNum := func(i int) string {
		n := i + 1
		if n < 10 {
			return string(rune('0' + n))
		}
		return string(rune('0'+n/10)) + string(rune('0'+n%10))
	}

	// Window [lo, hi] around the active season that fits maxWidth.
	// Widths are raw cells: 9 for "season   ", 5 per gap, digits per
	// tab, 1 per overflow marker.
	lo, hi := activeSeasonIndex, activeSeasonIndex
	width := func(lo, hi int) int {
		w := 9
		parts := hi - lo + 1
		if lo > 0 {
			parts++ // ‹ marker
		}
		if hi < seasonCount-1 {
			parts++ // › marker
		}
		w += parts // 1 cell per marker/tab minimum handled below
		for i := lo; i <= hi; i++ {
			w += len(seasonNum(i)) - 1
		}
		w += 5 * (parts - 1)
		return w
	}
	fits := func(lo, hi int) bool {
		return maxWidth <= 0 || width(lo, hi) <= maxWidth
	}
	for (lo > 0 || hi < seasonCount-1) && func() bool {
		// Expand the side hiding more seasons first (ties: right),
		// stopping when neither side fits.
		leftHidden, rightHidden := lo, seasonCount-1-hi
		if rightHidden >= leftHidden && hi < seasonCount-1 && fits(lo, hi+1) {
			hi++
			return true
		}
		if lo > 0 && fits(lo-1, hi) {
			lo--
			return true
		}
		if hi < seasonCount-1 && fits(lo, hi+1) {
			hi++
			return true
		}
		return false
	}() {
	}

	var labelParts []string
	var underlineParts []string
	if lo > 0 {
		labelParts = append(labelParts, st.Dim.Render("‹"))
		underlineParts = append(underlineParts, " ")
	}
	for i := lo; i <= hi; i++ {
		numStr := seasonNum(i)
		if i == activeSeasonIndex {
			labelParts = append(labelParts, st.ActiveTab.Render(numStr))
			underlineParts = append(underlineParts, st.Underline.Render(strings.Repeat("─", len(numStr))))
		} else {
			labelParts = append(labelParts, st.InactiveTab.Render(numStr))
			underlineParts = append(underlineParts, strings.Repeat(" ", len(numStr)))
		}
	}
	if hi < seasonCount-1 {
		labelParts = append(labelParts, st.Dim.Render("›"))
		underlineParts = append(underlineParts, " ")
	}

	prefix := st.Dim.Render("season") + "   "
	prefixUnderline := strings.Repeat(" ", lipgloss.Width(prefix))
	line1 := prefix + strings.Join(labelParts, "     ")
	line2 := prefixUnderline + strings.Join(underlineParts, "     ")

	return line1 + "\n" + line2
}
