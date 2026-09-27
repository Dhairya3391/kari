package termimg

import (
	"bytes"
	"fmt"
	"image"
	"image/jpeg"

	"github.com/BourgeoisBear/rasterm"
)

// JPEG qualities per surface: small poster boxes hide compression, but a
// fullscreen reader page puts line art and screentones on display, where
// q90 ringing around sharp edges reads as blur. q95 costs more bytes per
// page and is worth it there; posters stay at q90.
const (
	itermPosterQuality = 90
	itermPageQuality   = 95
)

// renderIterm encodes img via the iTerm2 inline-images protocol and wraps
// it with overlayInline so it composes with Bubble Tea's redraw model.
// Width is requested in terminal cells with aspect preserved; JPEG keeps
// the base64 payload small (photographic posters compress far better than
// Sixel RLE). The full-resolution pixels are transmitted and the terminal
// scales in hardware, so this path is already at full quality.
func renderIterm(img image.Image, cellW, cellH int) (string, error) {
	var jpg bytes.Buffer
	if err := jpeg.Encode(&jpg, img, &jpeg.Options{Quality: itermPosterQuality}); err != nil {
		return "", fmt.Errorf("termimg: jpeg encode failed: %w", err)
	}
	opts := rasterm.ItermImgOpts{
		Width:         fmt.Sprintf("%d", cellW),
		DisplayInline: true,
		Size:          int64(jpg.Len()),
	}
	var buf bytes.Buffer
	if err := rasterm.ItermCopyFileInlineWithOptions(&buf, &jpg, opts); err != nil {
		return "", fmt.Errorf("termimg: iterm encode failed: %w", err)
	}
	return overlayInline(placeholderBlock(cellW, cellH), buf.Bytes(), cellH), nil
}
