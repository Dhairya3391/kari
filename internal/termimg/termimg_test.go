package termimg

import (
	"image"
	"image/color"
	"strings"
	"testing"
)

// testImage builds a small non-trivial image for render tests.
func testImage() image.Image {
	img := image.NewRGBA(image.Rect(0, 0, 64, 96))
	for y := 0; y < 96; y++ {
		for x := 0; x < 64; x++ {
			img.Set(x, y, color.RGBA{uint8(x * 4), uint8(y * 2), 128, 0xff})
		}
	}
	return img
}

// lineCount returns the number of lines in a rendered block.
func lineCount(s string) int {
	if s == "" {
		return 0
	}
	return strings.Count(s, "\n") + 1
}

func TestProtocolString(t *testing.T) {
	cases := map[Protocol]string{
		ProtocolNone:   "none",
		ProtocolBlocks: "blocks",
		ProtocolSixel:  "sixel",
		ProtocolIterm:  "iterm",
		ProtocolKitty:  "kitty",
	}
	for p, want := range cases {
		if got := p.String(); got != want {
			t.Errorf("protocol %d String() = %q, want %q", int(p), got, want)
		}
	}
}

func TestDetectOverride(t *testing.T) {
	cases := map[string]Protocol{
		"kitty":  ProtocolKitty,
		"iterm":  ProtocolIterm,
		"iterm2": ProtocolIterm,
		"sixel":  ProtocolSixel,
		"blocks": ProtocolBlocks,
		"none":   ProtocolNone,
		"off":    ProtocolNone,
	}
	for raw, want := range cases {
		t.Setenv("KARI_IMG_PROTOCOL", raw)
		if got := Detect(); got != want {
			t.Errorf("Detect() with override %q = %v, want %v", raw, got, want)
		}
	}
}

func TestRenderLineContract(t *testing.T) {
	img := testImage()
	const cellW, cellH = 12, 8
	for _, p := range []Protocol{ProtocolKitty, ProtocolIterm, ProtocolSixel, ProtocolBlocks} {
		out, err := Render(img, p, cellW, cellH, 7, 0, 0)
		if err != nil {
			t.Errorf("Render(%v) failed: %v", p, err)
			continue
		}
		if got := lineCount(out); got != cellH {
			t.Errorf("Render(%v) returned %d lines, want %d", p, got, cellH)
		}
	}
}

func TestRenderInvalidSize(t *testing.T) {
	if _, err := Render(testImage(), ProtocolBlocks, 0, 8, 1, 0, 0); err == nil {
		t.Error("Render with zero width should fail")
	}
}

func TestRenderNoneEmpty(t *testing.T) {
	out, err := Render(testImage(), ProtocolNone, 12, 8, 1, 0, 0)
	if err != nil || out != "" {
		t.Errorf("Render(ProtocolNone) = %q, %v; want empty, nil", out, err)
	}
}

func TestSixelMagic(t *testing.T) {
	out, err := renderSixel(testImage(), 12, 8, 0, 0)
	if err != nil {
		t.Fatalf("renderSixel failed: %v", err)
	}
	if !strings.Contains(out, "\x1bP0;1q") {
		t.Error("sixel output missing DECSIXEL introducer")
	}
	if !strings.Contains(out, "\x1b\\") {
		t.Error("sixel output missing ST terminator")
	}
}

func TestItermMagic(t *testing.T) {
	out, err := renderIterm(testImage(), 12, 8)
	if err != nil {
		t.Fatalf("renderIterm failed: %v", err)
	}
	if !strings.Contains(out, "\x1b]1337;File=") {
		t.Error("iterm output missing inline-image header")
	}
}

func TestCleanupOnlyKitty(t *testing.T) {
	if got := ProtocolKitty.Cleanup(3); got == "" {
		t.Error("kitty Cleanup should be non-empty")
	}
	for _, p := range []Protocol{ProtocolNone, ProtocolBlocks, ProtocolSixel, ProtocolIterm} {
		if !p.NeedsCleanup() {
			continue
		}
		t.Errorf("protocol %v should not need cleanup", p)
	}
	if got := ProtocolSixel.Cleanup(3); got != "" {
		t.Errorf("sixel Cleanup = %q, want empty", got)
	}
	if got := ProtocolKitty.DeleteAll(); got == "" {
		t.Error("kitty DeleteAll should be non-empty")
	}
	if got := ProtocolBlocks.DeleteAll(); got != "" {
		t.Errorf("blocks DeleteAll = %q, want empty", got)
	}
}

func TestFitPixelsCaps(t *testing.T) {
	huge := image.NewRGBA(image.Rect(0, 0, 4000, 5000))
	pxW, pxH := fitPixels(huge, 200, 100, 8, 16)
	if pxW > maxSixelW || pxH > maxSixelH {
		t.Errorf("fitPixels = %dx%d, exceeds cap %dx%d", pxW, pxH, maxSixelW, maxSixelH)
	}
	if pxW <= 0 || pxH <= 0 {
		t.Errorf("fitPixels = %dx%d, want positive", pxW, pxH)
	}
}

// A flat mid-gray field must dither into a mix of adjacent palette
// entries (no banding into one flat index), while pure black/white
// fields stay exactly one index each (crisp line art preserved).
func TestToPalettedDithersMidtones(t *testing.T) {
	flat := func(v uint8) *image.Paletted {
		img := image.NewRGBA(image.Rect(0, 0, 16, 16))
		for y := 0; y < 16; y++ {
			for x := 0; x < 16; x++ {
				img.Set(x, y, color.RGBA{v, v, v, 0xff})
			}
		}
		return toPaletted(img)
	}
	distinct := func(p *image.Paletted) int {
		seen := make(map[uint8]struct{})
		for _, idx := range p.Pix {
			seen[idx] = struct{}{}
		}
		return len(seen)
	}
	if n := distinct(flat(128)); n < 2 {
		t.Errorf("mid-gray dithered to %d palette entries, want a mix", n)
	}
	if n := distinct(flat(0)); n != 1 {
		t.Errorf("black dithered to %d entries, want exactly 1", n)
	}
	if n := distinct(flat(255)); n != 1 {
		t.Errorf("white dithered to %d entries, want exactly 1", n)
	}
}

func TestNearestSixelIndexRange(t *testing.T) {
	for _, c := range []color.RGBA{{0, 0, 0, 255}, {255, 255, 255, 255}, {200, 30, 90, 255}, {128, 128, 128, 255}} {
		idx := nearestSixelIndex(c.R, c.G, c.B)
		if int(idx) >= len(sixelPalette) {
			t.Errorf("index %d out of palette range", idx)
		}
	}
	if len(sixelPalette) != 256 {
		t.Errorf("sixel palette size = %d, want 256", len(sixelPalette))
	}
}

func TestRenderPageLineContract(t *testing.T) {
	img := testImage()
	const boxW, boxH = 40, 20
	for _, p := range []Protocol{ProtocolKitty, ProtocolIterm, ProtocolSixel, ProtocolBlocks} {
		out, err := RenderPage(img, p, boxW, boxH, 9, 0, 0)
		if err != nil {
			t.Errorf("RenderPage(%v) failed: %v", p, err)
			continue
		}
		if got := lineCount(out); got != boxH {
			t.Errorf("RenderPage(%v) returned %d lines, want %d", p, got, boxH)
		}
		if !strings.Contains(out, readerBGOn) {
			t.Errorf("RenderPage(%v) missing black background", p)
		}
	}
}

func TestRenderPageInvalid(t *testing.T) {
	if _, err := RenderPage(testImage(), ProtocolBlocks, 0, 10, 1, 0, 0); err == nil {
		t.Error("RenderPage with zero width should fail")
	}
	if out, err := RenderPage(testImage(), ProtocolNone, 10, 10, 1, 0, 0); err != nil || out != "" {
		t.Errorf("RenderPage(ProtocolNone) = %q, %v; want empty, nil", out, err)
	}
}

// TestOverlayPositionAgnostic verifies the save/restore recipe: no
// absolute column moves (CR) and no row-count-dependent parking, so the
// block composes mid-line beside text and under outer padding.
func TestOverlayPositionAgnostic(t *testing.T) {
	for _, p := range []Protocol{ProtocolSixel, ProtocolIterm} {
		out, err := Render(testImage(), p, 12, 8, 7, 0, 0)
		if err != nil {
			t.Fatalf("Render(%v) failed: %v", p, err)
		}
		if !strings.Contains(out, "\x1b7") || !strings.Contains(out, "\x1b8") {
			t.Errorf("Render(%v) missing save/restore", p)
		}
		if strings.Contains(out, "\r") {
			t.Errorf("Render(%v) contains CR (column-absolute, breaks mid-line use)", p)
		}
		if strings.Contains(out, "\x1b[1A") {
			t.Errorf("Render(%v) contains legacy row park", p)
		}
		if got := lineCount(out); got != 8 {
			t.Errorf("Render(%v) = %d lines, want 8", p, got)
		}
	}
}

// TestSixelEnvHint pins conservative Sixel detection: stock xterm builds
// (notably macOS Terminal.app as xterm-256color) must not claim Sixel,
// or raw sixel bytes print as garbage. Those fall through to blocks.
func TestSixelEnvHint(t *testing.T) {
	cases := map[string]bool{
		"mlterm":          true,
		"foot":            true,
		"xterm-sixel":     true,
		"xterm":           false,
		"xterm-256color":  false,
		"screen-256color": false,
	}
	for term, want := range cases {
		t.Setenv("TERM", term)
		if got := sixelEnvHint(); got != want {
			t.Errorf("sixelEnvHint(%q) = %v, want %v", term, got, want)
		}
	}
}

// TestAutocontrastPage pins the wash-to-range mapping: gray paper goes
// white, soft lines go black, flat images pass through untouched.
func TestAutocontrastPage(t *testing.T) {
	washed := image.NewRGBA(image.Rect(0, 0, 8, 8))
	for y := 0; y < 8; y++ {
		for x := 0; x < 8; x++ {
			v := uint8(60)
			if x < 2 {
				v = 200
			}
			washed.Set(x, y, color.RGBA{v, v, v, 0xff})
		}
	}
	got := autocontrastPage(washed)
	lo, hi := uint8(255), uint8(0)
	for y := 0; y < 8; y++ {
		for x := 0; x < 8; x++ {
			r, _, _, _ := got.At(x, y).RGBA()
			v := uint8(r >> 8)
			lo, hi = min(lo, v), max(hi, v)
		}
	}
	if lo > 8 || hi < 247 {
		t.Errorf("range = [%d,%d], want near [0,255]", lo, hi)
	}

	flat := image.NewRGBA(image.Rect(0, 0, 4, 4))
	for y := 0; y < 4; y++ {
		for x := 0; x < 4; x++ {
			flat.Set(x, y, color.RGBA{128, 128, 128, 0xff})
		}
	}
	if out := autocontrastPage(flat); !equalGray(out, 128) {
		t.Errorf("flat image must pass through")
	}
}

func equalGray(img image.Image, want uint8) bool {
	b := img.Bounds()
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			r, _, _, _ := img.At(x, y).RGBA()
			if uint8(r>>8) != want {
				return false
			}
		}
	}
	return true
}

// TestCleanupTransmitFree pins cleanup to pure delete commands: emitting
// image data for cleanup made terminals answer EINVAL, and those error
// responses land on stdin as typed garbage in the search box.
func TestCleanupTransmitFree(t *testing.T) {
	out := ProtocolKitty.Cleanup(3)
	if !strings.Contains(out, "i=3") {
		t.Errorf("Cleanup missing image id: %q", out)
	}
	if strings.Contains(out, "a=T") {
		t.Errorf("Cleanup must not transmit image data: %q", out)
	}
}
