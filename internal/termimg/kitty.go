package termimg

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"image"
	"image/png"
	"strings"

	"github.com/BourgeoisBear/rasterm"
)

// kittyChunkSize is the max base64 payload bytes per escape sequence,
// mandated by the Kitty graphics protocol.
const kittyChunkSize = 4096

// DeleteKitty returns the (zero-width, invisible) escape sequence that tells
// the terminal to remove any placement of the given image id. Callers must
// send this whenever a slot that might be holding a Kitty image stops being
// shown — e.g. navigating to a different screen, or replacing it with a
// smaller image or with "no image available" text — otherwise the old
// placement is a persistent overlay that has nothing left telling the
// terminal to remove it, and it just sits on screen indefinitely (this is
// what caused stale/duplicate posters to linger behind other screens).
//
// Deliberately a single `a=d,d=i` form: the wider scope variants
// (`d=I`, `d=p`, `d=A`) EINVAL on terminals with partial Kitty
// implementations, and those error responses land on stdin as typed
// garbage in the search box.
func DeleteKitty(imageID uint32) string {
	if imageID == 0 {
		return fmt.Sprintf("%sa=d,d=a;%s", rasterm.KITTY_IMG_HDR, rasterm.KITTY_IMG_FTR)
	}
	return fmt.Sprintf("%sa=d,d=i,i=%d;%s",
		rasterm.KITTY_IMG_HDR, imageID, rasterm.KITTY_IMG_FTR,
	)
}

// DeleteAllKitty returns the escape sequence to delete all visible image placements.
func DeleteAllKitty() string {
	return fmt.Sprintf("%sa=d,d=a;%s",
		rasterm.KITTY_IMG_HDR, rasterm.KITTY_IMG_FTR,
	)
}

// renderKitty encodes img via the Kitty graphics protocol, tagged with
// imageID, into a string with exactly cellH lines so it composes safely
// with Bubble Tea's line-based redraw accounting.
//
// Three things make naively using rasterm.KittyWriteImage here unsafe:
//
//  1. Kitty's default cursor policy moves the cursor to just past the last
//     cell of the placed image. But the escape sequence carrying that
//     command is itself a single line of text with no embedded newlines, so
//     Bubble Tea's renderer — which tracks how much to redraw next frame by
//     counting lines of text it wrote — thinks exactly one line changed,
//     while the terminal just moved the cursor down `cellH` rows. That
//     mismatch is what corrupted the whole screen on every re-render before.
//     The fix is the graphics protocol's `C=1` key, which tells Kitty not to
//     move the cursor at all, combined with manually padding out cellH-1
//     blank lines ourselves so Bubble Tea's own line count already matches
//     the image's real height.
//  2. The image escape sequence (base64 PNG, easily tens of KB) must never
//     pass through a lipgloss Style with Width() set — lipgloss word-wraps
//     content wider than that Width, and since the sequence is one
//     unbroken run with no spaces, a wrap would slice straight through it.
//     That's the caller's responsibility (see view_helpers.go), not this
//     function's.
//  3. Reusing the same imageID across calls without deleting the old
//     placement first stacks a new image on top of the old one rather than
//     replacing it — visible as leftover pixels around the edges whenever
//     the new image is smaller than the last one shown in that slot. So
//     every call deletes imageID's previous placement before drawing.
func renderKitty(img image.Image, cellW, cellH int, imageID uint32, termCols, termRows int) (string, error) {
	// Downscale to display size before encoding: transmitting source
	// pixels for multi-megapixel uploads turns every frame into
	// megabytes of escape sequences that terminals silently drop
	// (blank reader on large pages), while the on-screen box only ever
	// shows cellW x cellH cells worth. termCols/termRows are the live
	// terminal dimensions, used to measure the real cell size — encoding
	// at the assumed 8x16 fallback size and letting the terminal upscale
	// is what made every image render soft. Same cap discipline as the
	// Sixel path (roomier: PNG compresses better than Sixel RLE);
	// never upscales.
	cw, ch := cellPixels(termCols, termRows)
	pxW, pxH := fitKittyPixels(img, cellW, cellH, cw, ch)
	small := img
	if pxW < img.Bounds().Dx() || pxH < img.Bounds().Dy() {
		small = resizeBox(img, pxW, pxH)
	}
	var pngBuf bytes.Buffer
	enc := png.Encoder{CompressionLevel: png.BestSpeed}
	if err := enc.Encode(&pngBuf, small); err != nil {
		return "", fmt.Errorf("termimg: png encode failed: %w", err)
	}
	b64 := base64.StdEncoding.EncodeToString(pngBuf.Bytes())

	opts := rasterm.KittyImgOpts{DstCols: uint32(cellW), DstRows: uint32(cellH), ImageId: imageID}

	var seq strings.Builder
	seq.WriteString(DeleteKitty(imageID))
	for i := 0; i < len(b64); i += kittyChunkSize {
		end := i + kittyChunkSize
		if end > len(b64) {
			end = len(b64)
		}
		more := "1"
		if end == len(b64) {
			more = "0"
		}
		if i == 0 {
			seq.WriteString(opts.ToHeader("a=T", "f=100", "t=d", "C=1", "m="+more))
		} else {
			seq.WriteString(rasterm.KITTY_IMG_HDR + "m=")
			seq.WriteString(more)
			seq.WriteString(";")
		}
		seq.WriteString(b64[i:end])
		seq.WriteString(rasterm.KITTY_IMG_FTR)
	}

	// The escape sequence itself has no printable characters, so anything
	// measuring this string's width (lipgloss.JoinHorizontal, when placing
	// text next to it) sees ~0 and doesn't reserve any columns for the
	// image — the next block then gets placed starting at the same column
	// the image occupies, overlapping it. Padding with cellW real spaces
	// makes the line's measured width match its actual on-screen width; the
	// terminal just treats them as an ordinary cursor advance since C=1
	// already stopped it from moving on its own.
	seq.WriteString(strings.Repeat(" ", cellW))

	if cellH > 1 {
		seq.WriteString(strings.Repeat("\n", cellH-1))
	}
	return seq.String(), nil
}
