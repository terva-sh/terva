package build

import (
	"bytes"
	"hash/crc32"
	"image"
	"image/color"
	"image/png"
	"testing"
)

func crc32PNG(b []byte) uint32 { return crc32.ChecksumIEEE(b) }

// pngOfSize renders a w×h PNG so a test can feed normalizeAvatar a real image.
func pngOfSize(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			// A gradient, not a flat fill: a solid image compresses to almost
			// nothing, which would make a size assertion meaningless.
			img.Set(x, y, color.RGBA{R: uint8(x), G: uint8(y), B: uint8(x ^ y), A: 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode: %v", err)
	}
	return buf.Bytes()
}

// A portrait already within bounds is stored byte-for-byte: importing a normal
// card must not silently re-encode (and thus degrade) its image.
func TestNormalizeAvatarKeepsSmallImage(t *testing.T) {
	src := pngOfSize(t, 400, 400)
	out, note := normalizeAvatar(src)
	if !bytes.Equal(out, src) {
		t.Errorf("small avatar was re-encoded: %d bytes in, %d out", len(src), len(out))
	}
	if note != "" {
		t.Errorf("unexpected note for an in-bounds avatar: %q", note)
	}
}

// The case that motivated this: a wallpaper-sized portrait (the real card was
// 7680x2160) is downscaled to something a library grid can serve.
//
// The gradient here is a patterned one that PNG compresses BETTER at full size
// than the resampled copy does — so this also pins the rule that an
// over-dimension image is downscaled on pixel count alone, never waved through
// because the giant happened to encode smaller.
func TestNormalizeAvatarDownscalesOversized(t *testing.T) {
	src := pngOfSize(t, 3000, 1200)
	out, note := normalizeAvatar(src)
	if note == "" {
		t.Fatal("an oversized avatar should report what was done to it")
	}
	cfg, err := png.DecodeConfig(bytes.NewReader(out))
	if err != nil {
		t.Fatalf("downscaled avatar is not a decodable PNG: %v", err)
	}
	if cfg.Width > maxAvatarDim || cfg.Height > maxAvatarDim {
		t.Errorf("still oversized: %dx%d exceeds %d", cfg.Width, cfg.Height, maxAvatarDim)
	}
	// Aspect ratio preserved: 3000x1200 is 2.5:1, so 1024 wide → ~409 tall.
	if got, want := float64(cfg.Width)/float64(cfg.Height), 2.5; got < want-0.05 || got > want+0.05 {
		t.Errorf("aspect ratio not preserved: got %.3f, want %.3f (%dx%d)", got, want, cfg.Width, cfg.Height)
	}
}

// A decompression bomb declares huge dimensions to make the decoder allocate
// width*height*4. The portrait is dropped rather than decoded — nil bytes, and a
// note saying so. The card itself still imports.
func TestNormalizeAvatarDropsDecodeBomb(t *testing.T) {
	// A valid 1x1 PNG with its IHDR rewritten to claim 30000x30000 (900M pixels,
	// ~3.6 GiB decoded). DecodeConfig reads the header and never allocates that.
	src := pngOfSize(t, 1, 1)
	bomb := forgeDimensions(t, src, 30000, 30000)
	out, note := normalizeAvatar(bomb)
	if out != nil {
		t.Errorf("a decode bomb should be dropped, got %d bytes", len(out))
	}
	if note == "" {
		t.Error("dropping a portrait must be explained")
	}
}

// Bytes that carried a readable card but are not a decodable image: kept when
// small (a viewer may cope), dropped when large.
func TestNormalizeAvatarUndecodable(t *testing.T) {
	small := []byte("\x89PNG\r\n\x1a\n not really a png")
	if out, note := normalizeAvatar(small); !bytes.Equal(out, small) || note != "" {
		t.Errorf("a small undecodable avatar should be kept as-is, got %d bytes note=%q", len(out), note)
	}
	large := append([]byte("\x89PNG\r\n\x1a\n"), bytes.Repeat([]byte{0xab}, maxAvatarBytes+1)...)
	out, note := normalizeAvatar(large)
	if out != nil {
		t.Errorf("a large undecodable avatar should be dropped, got %d bytes", len(out))
	}
	if note == "" {
		t.Error("dropping a portrait must be explained")
	}
}

// forgeDimensions rewrites a PNG's IHDR width/height and fixes the chunk CRC, so
// a test can build a header that lies about its size without carrying megabytes.
func forgeDimensions(t *testing.T, data []byte, w, h uint32) []byte {
	t.Helper()
	out := append([]byte(nil), data...)
	// IHDR data begins at byte 16: 8-byte signature + 4-byte length + 4-byte type.
	const ihdr = 16
	put32(out[ihdr:], w)
	put32(out[ihdr+4:], h)
	put32(out[ihdr+13:], crc32PNG(out[ihdr-4:ihdr+13]))
	return out
}

func put32(b []byte, v uint32) {
	b[0], b[1], b[2], b[3] = byte(v>>24), byte(v>>16), byte(v>>8), byte(v)
}
