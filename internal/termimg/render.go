package termimg

import (
	"fmt"
	"image"
	"strings"
)

// Render encodes img for display in a box roughly cellW columns by cellH
// terminal rows. Every protocol here returns a string with exactly cellH
// lines (cellH-1 newlines) so it composes safely with Bubble Tea's
// line-based redraw accounting — see kitty.go's doc comment for why that
// matters and how ProtocolKitty achieves it despite being a native graphics
// protocol. imageID is only meaningful for ProtocolKitty: it must be a
// stable id for the on-screen "slot" this image occupies (e.g. one id for a
// search-results preview pane, a different one for a detail page), reused
// across calls so each redraw replaces the previous placement instead of
// stacking on top of it — see DeleteKitty. It returns an empty string (no
// error) for ProtocolNone, so callers can render unconditionally.
func Render(img image.Image, protocol Protocol, cellW, cellH int, imageID uint32) (string, error) {
	if cellW <= 0 || cellH <= 0 {
		return "", fmt.Errorf("termimg: invalid target size %dx%d", cellW, cellH)
	}

	switch protocol {
	case ProtocolKitty:
		return renderKitty(img, cellW, cellH, imageID)
	case ProtocolBlocks:
		return renderQuadrants(img, cellW, cellH), nil
	default:
		return "", nil
	}
}

// RenderFit is like Render but derives the row count from img's own aspect
// ratio instead of taking it as a parameter, so the picture is never
// stretched. Terminal character cells are roughly twice as tall as they are
// wide, so a naive colsxrows box (e.g. a "square" 26x13) distorts a portrait
// poster; this instead picks rows so the displayed box's real-world aspect
// ratio matches the source image, capped to maxRows (shrinking cols to
// match) if that would otherwise make the image too tall.
func RenderFit(img image.Image, protocol Protocol, maxCols, maxRows int, imageID uint32) (string, error) {
	b := img.Bounds()
	if b.Dx() == 0 || b.Dy() == 0 {
		return "", fmt.Errorf("termimg: empty image")
	}

	aspect := float64(b.Dy()) / float64(b.Dx()) // height/width
	cols := maxCols
	rows := int(float64(cols) * aspect / 2) // /2 because a cell is ~2x taller than wide
	if rows > maxRows {
		rows = maxRows
		cols = int(float64(rows) * 2 / aspect)
	}
	if cols < 1 {
		cols = 1
	}
	if rows < 1 {
		rows = 1
	}

	return Render(img, protocol, cols, rows, imageID)
}

// quadrantGlyphs maps a 4-bit mask (bit0=top-left, bit1=top-right,
// bit2=bottom-left, bit3=bottom-right; a set bit means that quadrant is
// "foreground") to the Unicode block-element character covering exactly
// that set of quadrants. This is the same 2x2-subpixel-per-cell trick tools
// like chafa use for their non-sixel output: doubling both the horizontal
// and vertical resolution of plain half-block art using only characters
// every terminal font already has.
var quadrantGlyphs = [16]rune{
	0b0000: ' ',
	0b0001: '▘',
	0b0010: '▝',
	0b0011: '▀',
	0b0100: '▖',
	0b0101: '▌',
	0b0110: '▞',
	0b0111: '▛',
	0b1000: '▗',
	0b1001: '▚',
	0b1010: '▐',
	0b1011: '▜',
	0b1100: '▄',
	0b1101: '▙',
	0b1110: '▟',
	0b1111: '█',
}

// renderQuadrants draws img into cols x rows terminal cells, treating each
// cell as a 2x2 grid of sub-pixels. For each cell it picks whichever of the
// 16 quadrant glyphs (plus two colors) best approximates the four
// sub-pixels, by trying every 2-way split of the 4 sub-pixels and keeping
// the split with the lowest color variance.
func renderQuadrants(img image.Image, cols, rows int) string {
	gw, gh := cols*2, rows*2
	grid := boxDownsample(img, gw, gh)

	var b strings.Builder
	for row := 0; row < rows; row++ {
		for col := 0; col < cols; col++ {
			tl := grid[(row*2)*gw+col*2]
			tr := grid[(row*2)*gw+col*2+1]
			bl := grid[(row*2+1)*gw+col*2]
			br := grid[(row*2+1)*gw+col*2+1]

			mask, fg, bg := bestSplit(tl, tr, bl, br)
			fr, fgc, fb := fg.clamp()
			brr, bgc, bbc := bg.clamp()
			fmt.Fprintf(&b, "\x1b[38;2;%d;%d;%dm\x1b[48;2;%d;%d;%dm%c", fr, fgc, fb, brr, bgc, bbc, quadrantGlyphs[mask])
		}
		b.WriteString("\x1b[0m")
		if row < rows-1 {
			b.WriteByte('\n')
		}
	}
	return b.String()
}

// bestSplit tries all 16 ways to divide 4 sub-pixels into a foreground and
// background group and returns the mask (bit order: tl, tr, bl, br) with
// the lowest within-group color variance, along with each group's mean
// color.
func bestSplit(tl, tr, bl, br avgColor) (mask int, fg, bg avgColor) {
	pixels := [4]avgColor{tl, tr, bl, br}

	bestCost := -1.0
	for m := 0; m < 16; m++ {
		var sumFG, sumBG avgColor
		var nFG, nBG int
		for i, p := range pixels {
			if m&(1<<i) != 0 {
				sumFG.add(p)
				nFG++
			} else {
				sumBG.add(p)
				nBG++
			}
		}
		meanFG := sumFG.mean(nFG)
		meanBG := sumBG.mean(nBG)

		cost := 0.0
		for i, p := range pixels {
			if m&(1<<i) != 0 {
				cost += p.sqDist(meanFG)
			} else {
				cost += p.sqDist(meanBG)
			}
		}

		if bestCost < 0 || cost < bestCost {
			bestCost = cost
			mask, fg, bg = m, meanFG, meanBG
		}
	}
	return mask, fg, bg
}

// avgColor is an unclamped, unrounded RGB accumulator/color used while
// averaging source pixels, so repeated summing doesn't lose precision to
// premature uint8 rounding.
type avgColor struct {
	r, g, b float64
}

func (c *avgColor) add(o avgColor) {
	c.r += o.r
	c.g += o.g
	c.b += o.b
}

func (c avgColor) mean(n int) avgColor {
	if n == 0 {
		return avgColor{}
	}
	return avgColor{c.r / float64(n), c.g / float64(n), c.b / float64(n)}
}

func (c avgColor) sqDist(o avgColor) float64 {
	dr, dg, db := c.r-o.r, c.g-o.g, c.b-o.b
	return dr*dr + dg*dg + db*db
}

func (c avgColor) clamp() (uint8, uint8, uint8) {
	return clamp8(c.r), clamp8(c.g), clamp8(c.b)
}

func clamp8(v float64) uint8 {
	if v < 0 {
		return 0
	}
	if v > 255 {
		return 255
	}
	return uint8(v + 0.5)
}

// boxDownsample resizes img to exactly w x h pixels by averaging every
// source pixel that falls under each destination cell (a proper area/box
// filter), rather than point-sampling a single nearest pixel. For the large
// reduction ratios typical here (e.g. a 500x750 poster down to a ~50x50
// sub-pixel grid), area averaging captures real detail instead of aliasing
// noise the way nearest-neighbor point sampling does.
func boxDownsample(img image.Image, w, h int) []avgColor {
	src := img.Bounds()
	sw, sh := src.Dx(), src.Dy()

	out := make([]avgColor, w*h)
	for gy := 0; gy < h; gy++ {
		y0 := src.Min.Y + gy*sh/h
		y1 := src.Min.Y + (gy+1)*sh/h
		if y1 <= y0 {
			y1 = y0 + 1
		}
		for gx := 0; gx < w; gx++ {
			x0 := src.Min.X + gx*sw/w
			x1 := src.Min.X + (gx+1)*sw/w
			if x1 <= x0 {
				x1 = x0 + 1
			}

			var sum avgColor
			var n int
			for y := y0; y < y1; y++ {
				for x := x0; x < x1; x++ {
					r, g, b, _ := img.At(x, y).RGBA()
					sum.r += float64(r >> 8)
					sum.g += float64(g >> 8)
					sum.b += float64(b >> 8)
					n++
				}
			}
			out[gy*w+gx] = sum.mean(n)
		}
	}
	return out
}
