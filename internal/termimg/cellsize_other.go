//go:build !unix

package termimg

// cellPixelsUnix is a stub on non-Unix platforms (Windows, plan9, …),
// where TIOCGWINSZ does not exist. Callers fall back to assumedCellW/H.
func cellPixelsUnix(termCols, termRows int) (w, h int, ok bool) {
	return 0, 0, false
}
