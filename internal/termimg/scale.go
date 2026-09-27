package termimg

import (
	"image"
	"image/color"
)

// Assumed cell dimensions in pixels, used when the real ones cannot be
// measured (non-tty stdout, tmux without pass-through, non-Unix builds).
// 8x16 matches a typical 12pt monospace cell closely enough that a Sixel
// image sized on this assumption still lands within a row of its
// placeholder block.
const (
	assumedCellW = 8
	assumedCellH = 16
)

// maxSixelPixels caps the pixel dimensions of an encoded Sixel image, so
// a full manga page never turns into a multi-megabyte escape sequence.
// Sixel is paletted RLE: photographic posters compress well, sharp B/W
// line art less so, and capping width/height is what keeps worst-case
// output in the hundreds of KB. The overlay math in render.go accounts
// for the cap via an explicit cursor adjustment, so capping never breaks
// Bubble Tea's line accounting.
const (
	maxSixelW = 1000
	maxSixelH = 1400
)

// maxKittyPixels caps the pixel dimensions of a Kitty-encoded image. Kitty
// transmits PNG (which compresses line art far better than Sixel RLE) over
// chunked sequences the terminal reassembles, so it tolerates roughly twice
// the pixels Sixel does before sequences get large enough for terminals to
// silently drop them (blank reader on large pages). The taller height cap
// also preserves more width on long-strip webtoon pages, where fitting the
// whole strip into the cell box is what binds the width.
const (
	maxKittyW = 1600
	maxKittyH = 2400
)

// cellPixels returns the terminal cell size in pixels. termCols/termRows
// are the live terminal dimensions (0 when unknown); without a measurable
// terminal it returns the assumed defaults.
func cellPixels(termCols, termRows int) (w, h int) {
	if cw, ch, ok := cellPixelsUnix(termCols, termRows); ok && cw > 0 && ch > 0 {
		return cw, ch
	}
	return assumedCellW, assumedCellH
}

// fitPixels derives a pixel size for img that fits inside a cols x rows
// cell box on a cellW x cellH pixel grid, preserving aspect ratio and
// never upscaling. Sizes are capped by maxSixelPixels so a full manga
// page never turns into a multi-megabyte escape sequence; the overlay
// recipe is cursor-exact regardless of the rendered size, so capping
// never breaks Bubble Tea's line accounting.
func fitPixels(img image.Image, cols, rows, cellW, cellH int) (pxW, pxH int) {
	return fitPixelsCap(img, cols, rows, cellW, cellH, maxSixelW, maxSixelH)
}

// fitKittyPixels is fitPixels with the roomier Kitty cap: PNG over chunked
// sequences tolerates more pixels than Sixel RLE before terminals start
// dropping oversized transmits.
func fitKittyPixels(img image.Image, cols, rows, cellW, cellH int) (pxW, pxH int) {
	return fitPixelsCap(img, cols, rows, cellW, cellH, maxKittyW, maxKittyH)
}

// fitPixelsCap fits img inside the cols x rows cell box (never upscaling),
// then inside the maxW x maxH pixel cap. Callers must pass the cap for
// their own protocol: Sixel RLE bloats faster than Kitty PNG.
func fitPixelsCap(img image.Image, cols, rows, cellW, cellH, maxW, maxH int) (pxW, pxH int) {
	b := img.Bounds()
	srcW, srcH := b.Dx(), b.Dy()
	if srcW <= 0 || srcH <= 0 || cols <= 0 || rows <= 0 {
		return 1, 1
	}
	boxW, boxH := cols*cellW, rows*cellH
	pxW, pxH = srcW, srcH
	if pxW > boxW || pxH > boxH {
		scale := min(float64(boxW)/float64(pxW), float64(boxH)/float64(pxH))
		pxW = max(1, int(float64(pxW)*scale))
		pxH = max(1, int(float64(pxH)*scale))
	}
	if pxW > maxW || pxH > maxH {
		scale := min(float64(maxW)/float64(pxW), float64(maxH)/float64(pxH))
		pxW = max(1, int(float64(pxW)*scale))
		pxH = max(1, int(float64(pxH)*scale))
	}
	return pxW, pxH
}

// resizeBox scales img down to exactly w x h pixels with the same
// area/box filter renderQuadrants relies on, so large reductions keep
// real detail instead of nearest-neighbor aliasing. It only shrinks;
// callers must not pass dimensions larger than the source.
func resizeBox(img image.Image, w, h int) *image.RGBA {
	cells := boxDownsample(img, w, h)
	out := image.NewRGBA(image.Rect(0, 0, w, h))
	for i, c := range cells {
		r, g, b := c.clamp()
		out.Pix[i*4+0] = r
		out.Pix[i*4+1] = g
		out.Pix[i*4+2] = b
		out.Pix[i*4+3] = 0xff
	}
	return out
}

// autocontrastPage normalizes a manga page's washed-out scan range
// (gray paper, soft lines) to full black-white before display: the 1st
// and 99th luminance percentiles map to 0 and 255, applied as a
// per-channel linear stretch. Manga pages are near-gray, so this equals
// a luminance gain without hue shifts on real pages. Percentiles (not
// min/max) ignore speckles and JPEG edge outliers. Flat images
// (hi == lo) pass through untouched. It runs on full-resolution pixels
// before any protocol downscales, and its result rides the reader render
// cache, so the per-pixel pass costs once per page size.
func autocontrastPage(img image.Image) image.Image {
	b := img.Bounds()
	sw, sh := b.Dx(), b.Dy()
	if sw <= 0 || sh <= 0 {
		return img
	}
	const buckets = 64
	var hist [buckets]int
	total := 0
	// Strided sampling: enough statistics at a fraction of the cost.
	for y := b.Min.Y; y < b.Max.Y; y += 4 {
		for x := b.Min.X; x < b.Max.X; x += 4 {
			r, g, bl, _ := img.At(x, y).RGBA()
			lum := (19595*uint32(r>>8) + 38470*uint32(g>>8) + 7471*uint32(bl>>8)) >> 16
			hist[lum*buckets/256]++
			total++
		}
	}
	if total == 0 {
		return img
	}
	loCount, hiCount := (total+99)/100, (total+99)/100
	lo, hi := 0, buckets-1
	var acc int
	for i, n := range hist {
		acc += n
		if acc >= loCount {
			lo = i
			break
		}
	}
	acc = 0
	for i := buckets - 1; i >= 0; i-- {
		acc += hist[i]
		if acc >= hiCount {
			hi = i
			break
		}
	}
	if hi <= lo {
		return img
	}
	loF := float64(lo) * 256 / buckets
	hiF := float64(hi+1) * 256 / buckets
	gain := 255 / (hiF - loF)
	out := image.NewRGBA(image.Rect(0, 0, sw, sh))
	for y := 0; y < sh; y++ {
		for x := 0; x < sw; x++ {
			r, g, bl, a := img.At(b.Min.X+x, b.Min.Y+y).RGBA()
			// Per-channel linear stretch: manga pages are near-gray,
			// so this equals a luminance gain without hue shifts.
			out.Pix[(y*sw+x)*4+0] = clamp8((float64(r>>8) - loF) * gain)
			out.Pix[(y*sw+x)*4+1] = clamp8((float64(g>>8) - loF) * gain)
			out.Pix[(y*sw+x)*4+2] = clamp8((float64(bl>>8) - loF) * gain)
			out.Pix[(y*sw+x)*4+3] = uint8(a >> 8)
		}
	}
	return out
}

// sixelPalette is a fixed 256-color palette: a 6x6x6 color cube plus a
// 40-step grayscale ramp. Fixed (rather than adaptive median-cut) keeps
// encoding fast and dependency-free, and is plenty for posters and B/W
// manga pages, which is what this path renders.
var (
	sixelPalette    = buildSixelPalette()
	sixelPaletteRGB = buildSixelPaletteRGB()
)

func buildSixelPalette() color.Palette {
	p := make(color.Palette, 0, 256)
	steps := []uint8{0x00, 0x33, 0x66, 0x99, 0xcc, 0xff}
	for _, r := range steps {
		for _, g := range steps {
			for _, b := range steps {
				p = append(p, color.RGBA{r, g, b, 0xff})
			}
		}
	}
	for i := range 40 {
		v := uint8(i * 255 / 39)
		p = append(p, color.RGBA{v, v, v, 0xff})
	}
	return p
}

func buildSixelPaletteRGB() [256][3]uint8 {
	var rgb [256][3]uint8
	for i, c := range sixelPalette {
		r, g, b, _ := c.RGBA()
		rgb[i] = [3]uint8{uint8(r >> 8), uint8(g >> 8), uint8(b >> 8)}
	}
	return rgb
}

// toPaletted maps img's pixels to the sixelPalette with Floyd–Steinberg
// error diffusion. Uniform (non-dithered) quantization banded every
// gradient — skies, skin, vignettes — into flat stripes on the fixed
// 216-color cube; diffusing each pixel's residual to its not-yet-visited
// neighbors trades banding for high-frequency grain the eye reads as
// smooth tone. Serpentine scanning keeps the diffusion symmetric.
// Near-black/white pixels quantize exactly (zero residual), so B/W manga
// line art stays crisp.
func toPaletted(img image.Image) *image.Paletted {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	out := image.NewPaletted(b, sixelPalette)
	// Float working buffers carry the diffused error; alpha is ignored
	// (Sixel has no transparency — sources composite on black below).
	bufR := make([]float64, w*h)
	bufG := make([]float64, w*h)
	bufB := make([]float64, w*h)

	switch im := img.(type) {
	case *image.RGBA:
		for y := range h {
			rowOff := (b.Min.Y + y - im.Rect.Min.Y) * im.Stride
			for x := range w {
				pixOff := rowOff + (b.Min.X+x-im.Rect.Min.X)*4
				bufR[y*w+x] = float64(im.Pix[pixOff+0])
				bufG[y*w+x] = float64(im.Pix[pixOff+1])
				bufB[y*w+x] = float64(im.Pix[pixOff+2])
			}
		}
	case *image.NRGBA:
		for y := range h {
			rowOff := (b.Min.Y + y - im.Rect.Min.Y) * im.Stride
			for x := range w {
				pixOff := rowOff + (b.Min.X+x-im.Rect.Min.X)*4
				bufR[y*w+x] = float64(im.Pix[pixOff+0])
				bufG[y*w+x] = float64(im.Pix[pixOff+1])
				bufB[y*w+x] = float64(im.Pix[pixOff+2])
			}
		}
	default:
		for y := range h {
			for x := range w {
				r, g, bl, _ := img.At(b.Min.X+x, b.Min.Y+y).RGBA()
				bufR[y*w+x] = float64(r >> 8)
				bufG[y*w+x] = float64(g >> 8)
				bufB[y*w+x] = float64(bl >> 8)
			}
		}
	}

	for y := range h {
		leftToRight := y%2 == 0
		for i := range w {
			x := i
			if !leftToRight {
				x = w - 1 - i
			}
			idx := y*w + x
			oldR, oldG, oldB := bufR[idx], bufG[idx], bufB[idx]
			pal := nearestSixelIndex(clampU8(oldR), clampU8(oldG), clampU8(oldB))
			out.SetColorIndex(b.Min.X+x, b.Min.Y+y, pal)
			pr := float64(sixelPaletteRGB[pal][0])
			pg := float64(sixelPaletteRGB[pal][1])
			pb := float64(sixelPaletteRGB[pal][2])
			errR := oldR - pr
			errG := oldG - pg
			errB := oldB - pb
			// Floyd–Steinberg weights, mirrored on right-to-left rows.
			dx1 := 1
			if !leftToRight {
				dx1 = -1
			}
			spread := func(nx, ny int, factor float64) {
				if nx < 0 || nx >= w || ny < 0 || ny >= h {
					return
				}
				n := ny*w + nx
				bufR[n] += errR * factor
				bufG[n] += errG * factor
				bufB[n] += errB * factor
			}
			spread(x+dx1, y, 7.0/16)
			spread(x-dx1, y+1, 3.0/16)
			spread(x, y+1, 5.0/16)
			spread(x+dx1, y+1, 1.0/16)
		}
	}
	return out
}

// clampU8 folds a float channel into a byte for palette lookup.
func clampU8(v float64) uint8 {
	if v < 0 {
		return 0
	}
	if v > 255 {
		return 255
	}
	return uint8(v + 0.5)
}

// nearestSixelIndex returns the palette index closest to the given color:
// the gray ramp when all channels agree within tolerance, otherwise the
// enclosing 6x6x6 cube cell.
func nearestSixelIndex(r, g, b uint8) uint8 {
	lo, hi := min3(r, g, b), max3(r, g, b)
	if hi-lo < 12 {
		avg := (uint16(r) + uint16(g) + uint16(b)) / 3
		return uint8(216 + avg*39/255)
	}
	ri := (uint16(r)*5 + 127) / 255
	gi := (uint16(g)*5 + 127) / 255
	bi := (uint16(b)*5 + 127) / 255
	return uint8(ri*36 + gi*6 + bi)
}

func min3(a, b, c uint8) uint8 {
	return min(a, min(b, c))
}

func max3(a, b, c uint8) uint8 {
	return max(a, max(b, c))
}
