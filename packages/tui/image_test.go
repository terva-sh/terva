package tui

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"regexp"
	"strings"
	"testing"
)

func testPNG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{R: uint8(x), G: uint8(y), B: 128, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func testJPEG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{R: uint8(x), G: uint8(y), B: 128, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 90}); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

var kittyPayloadRE = regexp.MustCompile(`\x1b_G[^;]*;([^\x1b]*)\x1b\\`)

// kittyDecodePayload reassembles the base64 chunks from a kitty
// graphics escape sequence and returns the decoded raw bytes.
func kittyDecodePayload(t *testing.T, seq string) []byte {
	t.Helper()
	ms := kittyPayloadRE.FindAllStringSubmatch(seq, -1)
	if len(ms) == 0 {
		t.Fatalf("no kitty payload chunks found in sequence")
	}
	var b64 strings.Builder
	for _, m := range ms {
		b64.WriteString(m[1])
	}
	raw, err := base64.StdEncoding.DecodeString(b64.String())
	if err != nil {
		t.Fatalf("decode base64 payload: %v", err)
	}
	return raw
}

func TestRenderKittyReencodesJPEGToPNG(t *testing.T) {
	// A JPEG handed to the kitty renderer must be re-encoded to PNG,
	// because kitty's f=100 path only decodes PNG. Otherwise the terminal
	// reserves the cell rectangle but paints nothing, which is the
	// empty-box symptom.
	jpg := testJPEG(t, 40, 30)
	seq := renderKitty(jpg, 20, 10)
	if seq == "" {
		t.Fatal("renderKitty returned empty sequence")
	}
	raw := kittyDecodePayload(t, seq)
	pngMagic := []byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A}
	if len(raw) < 8 || !bytes.Equal(raw[:8], pngMagic) {
		t.Fatalf("kitty payload is not PNG; got prefix %x", raw[:min(8, len(raw))])
	}
}

func TestRenderKittyLeavesPNGUntouched(t *testing.T) {
	src := testPNG(t, 40, 30)
	seq := renderKitty(src, 20, 10)
	raw := kittyDecodePayload(t, seq)
	if !bytes.Equal(raw, src) {
		t.Fatal("PNG payload was needlessly re-encoded")
	}
}

func TestRowsForInlineImageRoundsUp(t *testing.T) {
	t.Setenv("TERVA_CELL_ASPECT", "")
	data := testPNG(t, 100, 51)
	got := RowsForInlineImage(data, 10, 0)
	// 51px high at 10 cells wide with a 2.0 cell aspect is 2.55 rows.
	// Rounding down to 2 lets following text overlap; we need 3.
	if got != 3 {
		t.Fatalf("RowsForInlineImage = %d, want 3", got)
	}
}

func TestRowsForInlineImageCellAspectOverride(t *testing.T) {
	data := testPNG(t, 100, 100)
	t.Setenv("TERVA_CELL_ASPECT", "1")
	if got := RowsForInlineImage(data, 10, 0); got != 10 {
		t.Fatalf("aspect=1 rows = %d, want 10", got)
	}
	t.Setenv("TERVA_CELL_ASPECT", "4")
	if got := RowsForInlineImage(data, 10, 0); got != 3 {
		t.Fatalf("aspect=4 rows = %d, want 3", got)
	}
}

func TestDetectImageProtocolPlaceholderAndVSCode(t *testing.T) {
	t.Setenv("TERVA_INLINE_IMAGES", "placeholder")
	if got := DetectImageProtocol(); got != ImageProtocolNone {
		t.Fatalf("placeholder protocol = %v, want none", got)
	}

	t.Setenv("TERVA_INLINE_IMAGES", "")
	t.Setenv("TERM_PROGRAM", "vscode")
	t.Setenv("KITTY_WINDOW_ID", "1")
	if got := DetectImageProtocol(); got != ImageProtocolNone {
		t.Fatalf("vscode auto protocol = %v, want none", got)
	}

	t.Setenv("TERVA_INLINE_IMAGES", "kitty")
	if got := DetectImageProtocol(); got != ImageProtocolKitty {
		t.Fatalf("forced kitty protocol = %v, want kitty", got)
	}
}
