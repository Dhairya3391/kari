package termimg

import (
	"bytes"
	"fmt"
	"image"
	"image/jpeg"
	"strings"

	"github.com/BourgeoisBear/rasterm"
)

// readerBG is the opaque black background SGR wrapping every reader page
// cell, so transparent terminal backgrounds never bleed through while
// reading. Applied as paint (spaces with background set) rather than a
// terminal setting the app could never reliably restore.
const (
	readerBGOn  = "\x1b[48;2;0;0;0m"
	readerReset = "\x1b[0m"
)

// RenderPage renders img as a fullscreen reader page inside a boxW x boxH
// cell box: aspect-fit, horizontally centered, on an opaque black
// background. It returns exactly boxH lines (boxH-1 newlines) so it
// composes with Bubble Tea's line-based redraw accounting like Render
// does. imageID follows Render's Kitty-slot contract; other protocols
// ignore it. termCols/termRows are the live terminal dimensions, used to
// measure the real cell size (see Render).
func RenderPage(img image.Image, protocol Protocol, boxW, boxH int, imageID uint32, termCols, termRows int) (string, error) {
	if boxW <= 0 || boxH <= 0 {
		return "", fmt.Errorf("termimg: invalid page box %dx%d", boxW, boxH)
	}
	b := img.Bounds()
	if b.Dx() == 0 || b.Dy() == 0 {
		return "", fmt.Errorf("termimg: empty image")
	}

	// Scans arrive gray and washed out; normalize contrast before any
	// protocol downscales so lines read clearly at cell resolution.
	img = autocontrastPage(img)

	cols, rows := fitBox(b.Dx(), b.Dy(), boxW, boxH)
	xOff := (boxW - cols) / 2

	var block string
	var err error
	switch protocol {
	case ProtocolKitty:
		block, err = renderKittyPage(img, cols, rows, boxW, xOff, imageID, termCols, termRows)
	case ProtocolIterm:
		block, err = renderItermPage(img, cols, rows, boxW, xOff)
	case ProtocolSixel:
		block, err = renderSixelPage(img, cols, rows, boxW, xOff, termCols, termRows)
	case ProtocolBlocks:
		block = renderBlocksPage(img, cols, rows, boxW, xOff)
	default:
		return "", nil
	}
	if err != nil {
		return "", err
	}

	// Opaque black filler for the rows below the image, so the whole
	// box is black edge to edge.
	var out strings.Builder
	out.WriteString(block)
	for r := rows; r < boxH; r++ {
		out.WriteByte('\n')
		out.WriteString(readerBGOn + strings.Repeat(" ", boxW) + readerReset)
	}
	return out.String(), nil
}

// fitBox derives image cols/rows preserving aspect ratio inside a
// boxW x boxH cell box. Cells are ~2x taller than wide, hence the /2.
func fitBox(srcW, srcH, boxW, boxH int) (cols, rows int) {
	aspect := float64(srcH) / float64(srcW)
	cols = boxW
	rows = int(float64(cols) * aspect / 2)
	if rows > boxH {
		rows = boxH
		cols = int(float64(rows) * 2 / aspect)
	}
	return max(1, cols), max(1, rows)
}

// bgLine wraps a rendered line in the reader background: leading pad,
// the line itself, trailing fill, all black. Re-emitting readerBGOn
// after the line (instead of assuming it survived) matters because
// block-art lines end in a reset of their own.
func bgLine(pad, line string, fill int) string {
	return readerBGOn + pad + readerReset + line + readerBGOn + strings.Repeat(" ", fill) + readerReset
}

// renderKittyPage wraps renderKitty's output in the reader background.
// Post-processing lines is safe here: the Kitty sequence moves the
// cursor nowhere (C=1), so prefixes/suffixes disturb nothing.
func renderKittyPage(img image.Image, cols, rows, boxW, xOff int, imageID uint32, termCols, termRows int) (string, error) {
	raw, err := renderKitty(img, cols, rows, imageID, termCols, termRows)
	if err != nil {
		return "", err
	}
	pad := strings.Repeat(" ", xOff)
	fill := max(0, boxW-xOff-cols)
	lines := strings.Split(raw, "\n")
	for i, ln := range lines {
		if ln == "" {
			lines[i] = readerBGOn + strings.Repeat(" ", boxW) + readerReset
		} else {
			lines[i] = bgLine(pad, ln, fill)
		}
	}
	return strings.Join(lines, "\n"), nil
}

// renderBlocksPage wraps quadrant-block art the same way. Block art is
// plain cells with no cursor motion, so the same wrap applies.
func renderBlocksPage(img image.Image, cols, rows, boxW, xOff int) string {
	raw := renderQuadrants(img, cols, rows)
	pad := strings.Repeat(" ", xOff)
	fill := max(0, boxW-xOff-cols)
	lines := strings.Split(raw, "\n")
	for i, ln := range lines {
		lines[i] = bgLine(pad, ln, fill)
	}
	return strings.Join(lines, "\n")
}

// bgPlaceholders builds the placeholder block for overlay protocols with
// the reader background baked in: xOff leading cells, the image area,
// then fill to boxW. The graphic overprints the middle afterwards.
func bgPlaceholders(cols, rows, boxW, xOff int) string {
	fill := max(0, boxW-xOff-cols)
	var b strings.Builder
	for row := 0; row < rows; row++ {
		b.WriteString(readerBGOn + strings.Repeat(" ", xOff+cols+fill) + readerReset)
		if row < rows-1 {
			b.WriteByte('\n')
		}
	}
	return b.String()
}

// renderSixelPage encodes img via DECSIXEL over a black placeholder
// block. See renderSixel for the encode path; the only page-specific
// parts are the background placeholders and the xOff overlay.
func renderSixelPage(img image.Image, cols, rows, boxW, xOff int, termCols, termRows int) (string, error) {
	cw, ch := cellPixels(termCols, termRows)
	pxW, pxH := fitPixels(img, cols, rows, cw, ch)
	small := img
	if pxW < img.Bounds().Dx() || pxH < img.Bounds().Dy() {
		small = resizeBox(img, pxW, pxH)
	}
	var buf bytes.Buffer
	if err := rasterm.SixelWriteImage(&buf, toPaletted(small)); err != nil {
		return "", fmt.Errorf("termimg: sixel encode failed: %w", err)
	}
	return overlayInlineX(bgPlaceholders(cols, rows, boxW, xOff), buf.Bytes(), rows, xOff, boxW), nil
}

// renderItermPage encodes img via iTerm2 inline images over a black
// placeholder block, sized in cells to the fitted box. Full-resolution
// pixels go over the wire for hardware downscaling; page quality (not
// poster quality) applies, since ringing around line art reads as blur.
func renderItermPage(img image.Image, cols, rows, boxW, xOff int) (string, error) {
	var jpg bytes.Buffer
	if err := jpeg.Encode(&jpg, img, &jpeg.Options{Quality: itermPageQuality}); err != nil {
		return "", fmt.Errorf("termimg: jpeg encode failed: %w", err)
	}
	opts := rasterm.ItermImgOpts{
		Width:         fmt.Sprintf("%d", cols),
		DisplayInline: true,
		Size:          int64(jpg.Len()),
	}
	var buf bytes.Buffer
	if err := rasterm.ItermCopyFileInlineWithOptions(&buf, &jpg, opts); err != nil {
		return "", fmt.Errorf("termimg: iterm encode failed: %w", err)
	}
	return overlayInlineX(bgPlaceholders(cols, rows, boxW, xOff), buf.Bytes(), rows, xOff, boxW), nil
}
