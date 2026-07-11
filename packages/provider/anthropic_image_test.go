package provider

import (
	"bytes"
	"encoding/binary"
	"hash/crc32"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"testing"
)

func makeRect(w, h int) image.Image {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	// Fill with a recognisable colour so a buggy resize that goes
	// transparent or zero-size is obvious.
	c := color.RGBA{R: 80, G: 200, B: 120, A: 255}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, c)
		}
	}
	return img
}

func encodePNG(t *testing.T, img image.Image) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	return buf.Bytes()
}

func encodeJPEG(t *testing.T, img image.Image) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 90}); err != nil {
		t.Fatalf("encode jpeg: %v", err)
	}
	return buf.Bytes()
}

func decodeConfig(t *testing.T, data []byte) image.Config {
	t.Helper()
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("decode config: %v", err)
	}
	return cfg
}

func TestAnthShrinkImage_PassesThroughWhenSmall(t *testing.T) {
	src := encodePNG(t, makeRect(800, 600))
	out, mime := anthShrinkImageBytesIfTooBig(src, "image/png")
	if !bytes.Equal(out, src) {
		t.Errorf("small image was rewritten; expected pass-through")
	}
	if mime != "image/png" {
		t.Errorf("mime changed unexpectedly: %s", mime)
	}
}

func TestAnthShrinkImage_CorrectsMislabeledMimeWhenSmall(t *testing.T) {
	// A JPEG that fits within the cap but is wrongly declared as PNG.
	// Anthropic 400s on the mismatch, so the builder must rewrite the
	// declared media type to match the bytes even without resizing.
	src := encodeJPEG(t, makeRect(800, 600))
	out, mime := anthShrinkImageBytesIfTooBig(src, "image/png")
	if !bytes.Equal(out, src) {
		t.Errorf("small image bytes were rewritten; expected pass-through")
	}
	if mime != "image/jpeg" {
		t.Errorf("mislabeled mime not corrected: got %s want image/jpeg", mime)
	}
}

func TestAnthBuildToolResultContent_RepairsMislabeledImageOnResume(t *testing.T) {
	// Simulates continuing a session whose transcript already carries a
	// tool_result image with the wrong declared media type (.png name,
	// JPEG bytes). The outbound request builder must rewrite the media
	// type to match the bytes so Anthropic accepts the resumed request.
	jpegBytes := encodeJPEG(t, makeRect(64, 64))
	blocks := []Content{
		TextBlock{Text: "screenshot"},
		ImageBlock{MimeType: "image/png", Data: jpegBytes},
	}
	raw, err := anthBuildToolResultContent(blocks)
	if err != nil {
		t.Fatalf("build tool result: %v", err)
	}
	if !bytes.Contains(raw, []byte(`"media_type":"image/jpeg"`)) {
		t.Fatalf("media type not repaired in outbound request: %s", raw)
	}
	if bytes.Contains(raw, []byte(`"media_type":"image/png"`)) {
		t.Fatalf("stale image/png media type still present: %s", raw)
	}
}

func TestAnthShrinkImage_DownscalesWhenTooWide(t *testing.T) {
	src := encodePNG(t, makeRect(4000, 1000))
	out, mime := anthShrinkImageBytesIfTooBig(src, "image/png")
	if bytes.Equal(out, src) {
		t.Fatalf("image was not resized")
	}
	if mime != "image/png" {
		t.Errorf("mime changed: %s", mime)
	}
	cfg := decodeConfig(t, out)
	if cfg.Width != anthMaxImageSide {
		t.Errorf("width: got %d want %d", cfg.Width, anthMaxImageSide)
	}
	// Aspect ratio preserved: 4000:1000 -> 2000:500.
	if cfg.Height != 500 {
		t.Errorf("height: got %d want 500", cfg.Height)
	}
}

func TestAnthShrinkImage_DownscalesWhenTooTall(t *testing.T) {
	src := encodePNG(t, makeRect(1500, 6000))
	out, _ := anthShrinkImageBytesIfTooBig(src, "image/png")
	cfg := decodeConfig(t, out)
	if cfg.Height != anthMaxImageSide {
		t.Errorf("height: got %d want %d", cfg.Height, anthMaxImageSide)
	}
	// 1500:6000 -> 500:2000.
	if cfg.Width != 500 {
		t.Errorf("width: got %d want 500", cfg.Width)
	}
}

func TestAnthShrinkImage_PreservesJPEGFormat(t *testing.T) {
	src := encodeJPEG(t, makeRect(3000, 2500))
	out, mime := anthShrinkImageBytesIfTooBig(src, "image/jpeg")
	if mime != "image/jpeg" {
		t.Errorf("mime should stay image/jpeg, got %s", mime)
	}
	cfg := decodeConfig(t, out)
	if cfg.Width > anthMaxImageSide || cfg.Height > anthMaxImageSide {
		t.Errorf("dimensions exceed cap: %dx%d", cfg.Width, cfg.Height)
	}
}

func TestAnthShrinkImage_BadDataReturnsOriginal(t *testing.T) {
	src := []byte("not an image at all")
	out, mime := anthShrinkImageBytesIfTooBig(src, "image/png")
	if !bytes.Equal(out, src) {
		t.Errorf("garbage input was mutated")
	}
	if mime != "image/png" {
		t.Errorf("mime was changed on bad input: %s", mime)
	}
}

// pngHeaderClaiming builds the smallest byte sequence image.DecodeConfig
// accepts as a PNG with the given dimensions: signature + a CRC-valid
// IHDR chunk (RGBA, 8-bit) and nothing else. The body is absent on
// purpose — a decode bomb is exactly a tiny file with a huge header, and
// the cap must reject it from the header alone, before image.Decode ever
// runs (a full decode of the claimed size would be the vulnerability).
func pngHeaderClaiming(t *testing.T, width, height uint32) []byte {
	t.Helper()
	var buf bytes.Buffer
	buf.Write([]byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'})
	ihdr := make([]byte, 13)
	binary.BigEndian.PutUint32(ihdr[0:4], width)
	binary.BigEndian.PutUint32(ihdr[4:8], height)
	ihdr[8] = 8  // bit depth
	ihdr[9] = 6  // color type RGBA
	ihdr[10] = 0 // compression
	ihdr[11] = 0 // filter
	ihdr[12] = 0 // interlace
	var length [4]byte
	binary.BigEndian.PutUint32(length[:], 13)
	buf.Write(length[:])
	chunk := append([]byte("IHDR"), ihdr...)
	buf.Write(chunk)
	var crc [4]byte
	binary.BigEndian.PutUint32(crc[:], crc32.ChecksumIEEE(chunk))
	buf.Write(crc[:])
	return buf.Bytes()
}

// TestAnthShrinkImage_MegapixelCapRejectsDecodeBomb: an image whose
// header claims an area over maxInputPixels must come back untouched —
// and fast, because the full decode (which for 50000×50000 RGBA would
// be a ~10 GB allocation) must never be attempted.
func TestAnthShrinkImage_MegapixelCapRejectsDecodeBomb(t *testing.T) {
	src := pngHeaderClaiming(t, 50000, 50000)
	// Precondition: the header alone parses to the claimed dimensions.
	cfg := decodeConfig(t, src)
	if cfg.Width != 50000 || cfg.Height != 50000 {
		t.Fatalf("synthetic header parsed as %dx%d", cfg.Width, cfg.Height)
	}
	out, mime := anthShrinkImageBytesIfTooBig(src, "image/png")
	if !bytes.Equal(out, src) {
		t.Error("over-cap image was not returned unchanged")
	}
	if mime != "image/png" {
		t.Errorf("mime changed on over-cap image: %s", mime)
	}
}

// TestAnthShrinkImage_UnderCapStillShrinks: the cap must not catch the
// legitimate oversize-but-decodable case the shrinker exists for.
func TestAnthShrinkImage_UnderCapStillShrinks(t *testing.T) {
	src := encodePNG(t, makeRect(3000, 100)) // 0.3 MP, over the side cap
	out, _ := anthShrinkImageBytesIfTooBig(src, "image/png")
	cfg := decodeConfig(t, out)
	if cfg.Width != anthMaxImageSide {
		t.Errorf("width: got %d want %d", cfg.Width, anthMaxImageSide)
	}
}
