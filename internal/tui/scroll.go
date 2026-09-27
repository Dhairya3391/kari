package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"

	"kari/internal/history"
	"kari/internal/provider"
)

// windowStart returns the first visible row index for a centered cursor
// window over total rows: the single scroll rule shared by windowRows,
// the episodes list, and the preview source table.
func windowStart(total, cursorPos, visible int) int {
	if visible < 3 {
		visible = 3
	}
	if total <= visible {
		return 0
	}
	if cursorPos < 0 {
		cursorPos = 0
	}
	if cursorPos > total-1 {
		cursorPos = total - 1
	}
	start := cursorPos - visible/2
	if start < 0 {
		start = 0
	}
	if maxStart := total - visible; start > maxStart {
		start = maxStart
	}
	return start
}

// windowRows keeps a cursor row visible inside a fixed-height viewport.
// rows is the full flat list (section headers included); cursorPos is the
// flat position of the selected row, or -1 when nothing is selected.
// Short lists pass through unchanged. It returns the visible slice plus
// how many rows were clipped above and below, so callers can render the
// standard scroll hints. The cursor stays centered in the viewport while
// it moves through the middle of the list — the window only shifts when
// the cursor would leave it — so single keypresses never jump the whole
// list (the old pin-to-bottom rule scrolled on every move, worst in live
// mode where group headers eat the small viewport).
func windowRows(rows []string, cursorPos, visible int) (out []string, above, below int) {
	if visible < 3 {
		visible = 3
	}
	if len(rows) <= visible {
		return rows, 0, 0
	}
	if cursorPos < 0 {
		cursorPos = 0
	}
	if cursorPos > len(rows)-1 {
		cursorPos = len(rows) - 1
	}
	start := windowStart(len(rows), cursorPos, visible)
	end := start + visible
	return rows[start:end], start, len(rows) - end
}

// windowHint renders one "↑ n more" / "↓ n more" marker, or "" when
// nothing was clipped on that side.
func windowHint(n int, up bool, st Styles) string {
	if n <= 0 {
		return ""
	}
	if up {
		return st.Dim.Render(fmt.Sprintf("  ↑ %d more", n))
	}
	return st.Dim.Render(fmt.Sprintf("  ↓ %d more", n))
}

// applyWindow slices rows to the viewport and wraps them with scroll
// markers. headerReserve is the lines already used above the list.
func applyWindow(rows []string, cursorPos, height, headerReserve int, st Styles) []string {
	visible := max(5, height-headerReserve)
	win, above, below := windowRows(rows, cursorPos, visible)
	var out []string
	if h := windowHint(above, true, st); h != "" {
		out = append(out, h)
	}
	out = append(out, win...)
	if h := windowHint(below, false, st); h != "" {
		out = append(out, h)
	}
	return out
}

// singleColumn finishes a narrow single-column screen: padded rows are
// trimmed back to their content and the block is centered, so lists sit
// in the middle of wide terminals instead of hugging the left edge.
// Wide blocks pass through via centerBlock unchanged.
func singleColumn(content string, width, maxW int) string {
	lines := strings.Split(content, "\n")
	for i, line := range lines {
		lines[i] = strings.TrimRight(line, " ")
	}
	return centerBlock(strings.Join(lines, "\n"), width, maxW)
}
func centerBlock(content string, width, maxW int) string {
	if maxW <= 0 || width <= 0 {
		return content
	}
	lines := strings.Split(content, "\n")
	// The widest visible line sets the block width; lines already at or
	// past the terminal width are left alone so nothing wraps.
	blockW := 0
	for _, line := range lines {
		if w := lipgloss.Width(line); w > blockW {
			blockW = w
		}
	}
	if blockW >= width || blockW > maxW {
		return content
	}
	gutter := strings.Repeat(" ", (width-blockW)/2)
	for i, line := range lines {
		if line != "" {
			lines[i] = gutter + line
		}
	}
	return strings.Join(lines, "\n")
}
func truncate(s string, maxLen int) string {
	if lipgloss.Width(s) <= maxLen {
		return s
	}
	runes := []rune(s)
	if len(runes) <= maxLen {
		return s
	}
	if maxLen <= 1 {
		return "…"
	}
	return string(runes[:maxLen-1]) + "…"
}
func formatContinuePosition(entry history.Entry) string {
	if entry.Mode == string(provider.ModeManga) {
		ch := "ch 1"
		if strings.TrimSpace(entry.EpisodeTitle) != "" {
			ch = "ch " + strings.TrimSpace(entry.EpisodeTitle)
		}
		if entry.PositionSecs > 0 && entry.DurationSecs > 0 {
			return fmt.Sprintf("%s · p%d/%d", ch, int(entry.PositionSecs), int(entry.DurationSecs))
		}
		return ch
	}

	if entry.Season > 0 && entry.Episode > 0 {
		return fmt.Sprintf("s%02de%02d", entry.Season, entry.Episode)
	}
	if entry.Episode > 0 {
		return fmt.Sprintf("ep %02d", entry.Episode)
	}

	if entry.PositionSecs > 0 && entry.DurationSecs > 0 {
		return fmt.Sprintf("%s/%s", formatDurationShort(entry.PositionSecs), formatDurationShort(entry.DurationSecs))
	}

	// A finished entry with no position data (tracker imports, legacy
	// rows) must not read "0%": the bar beside it is full.
	if entry.Complete {
		return "done"
	}
	return "0%"
}

func formatDurationShort(seconds float64) string {
	d := time.Duration(seconds) * time.Second
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
}

// wrapText breaks a long paragraph into lines fitting width, with an optional maxLines limit.
func wrapText(text string, width, maxLines int) string {
	if width <= 0 {
		width = 40
	}
	words := strings.Fields(text)
	if len(words) == 0 {
		return ""
	}

	var lines []string
	var curLine strings.Builder

	for _, word := range words {
		if curLine.Len() == 0 {
			curLine.WriteString(word)
		} else if curLine.Len()+1+len(word) <= width {
			curLine.WriteByte(' ')
			curLine.WriteString(word)
		} else {
			lines = append(lines, curLine.String())
			curLine.Reset()
			curLine.WriteString(word)
			if maxLines > 0 && len(lines) >= maxLines {
				lines[len(lines)-1] = truncate(lines[len(lines)-1], width-1) + "…"
				return strings.Join(lines, "\n")
			}
		}
	}
	if curLine.Len() > 0 {
		lines = append(lines, curLine.String())
	}
	if maxLines > 0 && len(lines) > maxLines {
		lines = lines[:maxLines]
		lines[len(lines)-1] = truncate(lines[len(lines)-1], width-1) + "…"
	}

	return strings.Join(lines, "\n")
}
