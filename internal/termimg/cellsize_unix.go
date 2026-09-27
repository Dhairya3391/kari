//go:build unix

package termimg

import (
	"golang.org/x/sys/unix"
)

// cellPixelsUnix queries the terminal for its pixel dimensions via
// TIOCGWINSZ and derives the cell size from the known column/row count.
// ok is false when stdout is not a tty or the kernel reports no pixel
// dimensions (e.g. inside tmux without pass-through), in which case the
// caller falls back to assumedCellW/H.
func cellPixelsUnix(termCols, termRows int) (w, h int, ok bool) {
	if termCols <= 0 || termRows <= 0 {
		return 0, 0, false
	}
	ws, err := unix.IoctlGetWinsize(1, unix.TIOCGWINSZ)
	if err != nil || ws.Col == 0 || ws.Row == 0 {
		return 0, 0, false
	}
	if ws.Xpixel == 0 || ws.Ypixel == 0 {
		return 0, 0, false
	}
	return int(ws.Xpixel) / int(ws.Col), int(ws.Ypixel) / int(ws.Row), true
}
