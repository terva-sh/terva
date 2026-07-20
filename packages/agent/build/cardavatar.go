package build

import (
	"bytes"
	"fmt"
	"image"
	"image/png"

	"golang.org/x/image/draw"
)

// Imported cards carry their portrait as the PNG's own pixels, and those pixels
// are whatever the card's author happened to export — a real card off chub.ai
// was observed at 7680x2160 / 19.6 MB, a wallpaper with a character sheet
// stapled to it. Storing that verbatim made avatar.png a 19.6 MB file that
// /media then re-served on every render of the library grid.
//
// So an oversized portrait is downscaled once, at import, and the small version
// is what lands in the library. This is the one seam every import path passes
// through (bytes from an upload, a server-local path, a fetched URL), so the
// bound holds for all three.
//
// The card DATA is never touched by any of this: ImportBytes has already parsed
// the `chara` chunk into card.json (the source of truth) before the pixels get
// here, so re-encoding the portrait cannot lose a field. The re-encoded avatar
// drops the now-redundant `chara` chunk with it, which is why export re-embeds
// the chunk from card.json (card.WritePNG) rather than trusting avatar.png.
const (
	// maxAvatarDim is the longest edge kept for a stored portrait. A character
	// avatar is rendered in a grid tile or a sheet header; 1024px is generous
	// for both and still leaves a file measured in hundreds of KB.
	maxAvatarDim = 1024
	// maxAvatarBytes is the size above which a portrait is re-encoded even when
	// its dimensions are already within bounds — a small-but-enormous PNG
	// (16-bit channels, no filtering) is just as bad to serve.
	maxAvatarBytes = 2 << 20 // 2 MiB
	// maxAvatarPixels bounds what will be DECODED at all. A PNG header is
	// untrusted input and a decompression bomb declares huge dimensions to make
	// the decoder allocate width*height*4 bytes. Past this, the portrait is
	// dropped rather than decoded — the card still imports, it just has no
	// avatar.
	maxAvatarPixels = 64 << 20 // 64M pixels ≈ 256 MiB decoded RGBA
)

// normalizeAvatar returns the PNG bytes to store as a card's portrait, plus a
// human-readable note when the input was changed or dropped ("" when the
// original is stored as-is).
//
// It never fails the import: a portrait that cannot be decoded is kept verbatim
// when it is small (the bytes parsed as a card, so something is there worth
// keeping) and dropped when it is not. nil bytes mean "store no avatar".
func normalizeAvatar(data []byte) ([]byte, string) {
	cfg, err := png.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		// Not decodable as a PNG image, yet it carried a readable card. Keep a
		// small one (a viewer may still cope), drop a large one rather than
		// serving megabytes of something we cannot even measure.
		if len(data) <= maxAvatarBytes {
			return data, ""
		}
		return nil, fmt.Sprintf("portrait dropped: %d bytes of undecodable PNG (%v)", len(data), err)
	}
	if int64(cfg.Width)*int64(cfg.Height) > maxAvatarPixels {
		return nil, fmt.Sprintf("portrait dropped: %dx%d exceeds the %d-pixel decode limit", cfg.Width, cfg.Height, maxAvatarPixels)
	}
	if cfg.Width <= maxAvatarDim && cfg.Height <= maxAvatarDim && len(data) <= maxAvatarBytes {
		return data, "" // already small enough on both axes
	}
	src, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		if len(data) <= maxAvatarBytes {
			return data, ""
		}
		return nil, fmt.Sprintf("portrait dropped: %d bytes that would not decode (%v)", len(data), err)
	}
	oversizedDims := cfg.Width > maxAvatarDim || cfg.Height > maxAvatarDim
	dst := scaleToFit(src, maxAvatarDim)
	var buf bytes.Buffer
	enc := png.Encoder{CompressionLevel: png.BestCompression}
	if err := enc.Encode(&buf, dst); err != nil {
		// Re-encode failed; fall back to the original only if it is not the very
		// thing we were trying to avoid storing.
		if len(data) <= maxAvatarBytes {
			return data, ""
		}
		return nil, fmt.Sprintf("portrait dropped: re-encode failed (%v)", err)
	}
	// When the DIMENSIONS were over the limit, keep the downscaled image even if
	// it did not come out smaller in bytes. Pixel count is its own cost — every
	// render decodes width*height*4 in the browser — and a highly-compressible
	// giant (a flat or heavily-patterned wallpaper) can encode smaller at full
	// size than a resampled copy does. Bounding only bytes would let exactly that
	// image through at 7680x2160.
	if !oversizedDims && buf.Len() >= len(data) {
		return data, "" // re-encode bought nothing and the size was already legal
	}
	b := dst.Bounds()
	return buf.Bytes(), fmt.Sprintf("portrait downscaled from %dx%d (%s) to %dx%d (%s)",
		cfg.Width, cfg.Height, humanBytes(len(data)), b.Dx(), b.Dy(), humanBytes(buf.Len()))
}

// humanBytes renders a size the way the warning's reader thinks about it — this
// string goes to a user in the Stage library, not to a log.
func humanBytes(n int) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%d KB", n/(1<<10))
	default:
		return fmt.Sprintf("%d bytes", n)
	}
}

// scaleToFit returns src resized so its longest edge is at most max, preserving
// aspect ratio. An image already within bounds is returned re-drawn at its own
// size (the caller only reaches here when a re-encode is wanted anyway).
func scaleToFit(src image.Image, max int) image.Image {
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	if w > max || h > max {
		if w >= h {
			h = h * max / w
			w = max
		} else {
			w = w * max / h
			h = max
		}
	}
	if w < 1 {
		w = 1
	}
	if h < 1 {
		h = 1
	}
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	// CatmullRom over the cheaper kernels: this runs once per import, and the
	// result is the portrait a user looks at for the life of the card.
	draw.CatmullRom.Scale(dst, dst.Bounds(), src, b, draw.Over, nil)
	return dst
}
