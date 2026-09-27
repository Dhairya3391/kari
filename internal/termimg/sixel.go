package termimg

import (
	"bytes"
	"fmt"
	"image"
	"strings"

	"github.com/BourgeoisBear/rasterm"
)

// Cursor-motion escapes used to overlay pixel graphics onto a placeholder
// block without disturbing Bubble Tea's line accounting. See
// overlayInlineX for the full recipe.
const (
	cuuFmt        = "\x1b[%dA" // cursor up N lines (no drawing)
	cubFmt        = "\x1b[%dD" // cursor back N columns (no drawing)
	saveCursor    = "\x1b7"    // DECSC: save cursor position
	restoreCursor = "\x1b8"    // DECRC: restore cursor position
)

// overlayInline wraps graphic bytes (a cursor-moving pixel protocol: Sixel
// or iTerm2) so they compose with Bubble Tea's line-based redraw model.
// placeholders is exactly cellH lines; the graphic is drawn over the
// placeholder area and the cursor ends exactly where printing the
// placeholders alone would have left it.
//
// Recipe (position-agnostic, so it works mid-line — e.g. a poster beside
// text — and under any outer padding, where absolute column moves like
// CR would misalign the graphic):
//
//	placeholders (exactly cellH lines, ending at column C0)
//	SAVE — remember (last line, C0)
//	CUU(cellH-1) — back to the first line (same column)
//	CUB(blockW-xOff) — to the graphic start column
//	GRAPHIC — drawn over the placeholders
//	RESTORE — cursor back to (last line, C0) no matter where the
//	  protocol left it
//
// Bubble Tea counts cellH lines; the terminal cursor moves exactly
// cellH lines net. The moves never emit cells, so the graphic is never
// overwritten. xOff offsets the graphic within a wider placeholder box
// (reader centering); blockW is the placeholder lines' visible width,
// which the cursor math is relative to.
func overlayInline(placeholders string, graphic []byte, cellH int) string {
	return overlayInlineX(placeholders, graphic, cellH, 0, placeholderWidth(placeholders))
}

// overlayInlineX is overlayInline with a horizontal offset and an
// explicit visible block width (placeholder lines carrying background
// escapes report a bogus byte length, so callers pass the real width).
func overlayInlineX(placeholders string, graphic []byte, cellH, xOff, blockW int) string {
	var b strings.Builder
	b.WriteString(placeholders)
	b.WriteString(saveCursor)
	if cellH > 1 {
		fmt.Fprintf(&b, cuuFmt, cellH-1)
	}
	fmt.Fprintf(&b, cubFmt, max(1, blockW-xOff))
	b.Write(graphic)
	b.WriteString(restoreCursor)
	return b.String()
}

// placeholderWidth recovers the block width from its first line, so the
// final park move matches the placeholders without threading another
// parameter through.
func placeholderWidth(placeholders string) int {
	if i := strings.IndexByte(placeholders, '\n'); i >= 0 {
		return i
	}
	return len(placeholders)
}

// placeholderBlock builds the cellW x cellH space block every inline
// protocol pads with, so measured width matches on-screen width for
// lipgloss.JoinHorizontal and Bubble Tea counts exactly cellH lines.
func placeholderBlock(cellW, cellH int) string {
	var b strings.Builder
	for row := 0; row < cellH; row++ {
		b.WriteString(strings.Repeat(" ", cellW))
		if row < cellH-1 {
			b.WriteByte('\n')
		}
	}
	return b.String()
}

// renderSixel encodes img for a cellW x cellH box via DECSIXEL and wraps
// it with overlayInline. Sixel needs a paletted image, so the source is
// resized to fit the box (capped by maxSixelPixels) and quantized to the
// fixed 256-color palette first.
func renderSixel(img image.Image, cellW, cellH int, termCols, termRows int) (string, error) {
	cw, ch := cellPixels(termCols, termRows)
	pxW, pxH := fitPixels(img, cellW, cellH, cw, ch)
	small := img
	if pxW < img.Bounds().Dx() || pxH < img.Bounds().Dy() {
		small = resizeBox(img, pxW, pxH)
	}
	var buf bytes.Buffer
	if err := rasterm.SixelWriteImage(&buf, toPaletted(small)); err != nil {
		return "", fmt.Errorf("termimg: sixel encode failed: %w", err)
	}
	return overlayInline(placeholderBlock(cellW, cellH), buf.Bytes(), cellH), nil
}
