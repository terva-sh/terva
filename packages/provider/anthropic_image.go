package provider

import (
	"bytes"
	"image"
	"image/jpeg"
	"image/png"
	"strings"

	xdraw "golang.org/x/image/draw"

	// Register decoders for the formats we want to accept on input.
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
)

// anthMaxImageSide is Anthropic's per-image dimension cap when a
// request contains more than one image. Single-image requests are
// allowed up to 8000 px, but terva conversations routinely include
// multiple images (a screenshot here, a tool result image there)
// so we always normalise down to the stricter limit. 2000 px on the
// longest side is well below their cap and still readable for the
// kinds of screenshots / charts the model usually consumes.
const anthMaxImageSide = 2000

// anthSniffImageMIME returns the media type implied by an image's
// leading magic bytes, independent of any declared type or stdlib
// decoder registration. Returns "" when the signature is unrecognized,
// leaving the caller's declared MIME untouched.
func anthSniffImageMIME(data []byte) string {
	switch {
	case len(data) >= 8 && bytes.Equal(data[:8], []byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A}):
		return "image/png"
	case len(data) >= 3 && data[0] == 0xFF && data[1] == 0xD8 && data[2] == 0xFF:
		return "image/jpeg"
	case len(data) >= 6 && (bytes.Equal(data[:6], []byte("GIF87a")) || bytes.Equal(data[:6], []byte("GIF89a"))):
		return "image/gif"
	case len(data) >= 12 && bytes.Equal(data[:4], []byte("RIFF")) && bytes.Equal(data[8:12], []byte("WEBP")):
		return "image/webp"
	}
	return ""
}

// maxInputPixels bounds the SOURCE image area we are willing to fully
// decode for resizing: 64 megapixels (an 8000×8000 — Anthropic's own
// single-image dimension cap — decodes to ~256 MB of RGBA, the most we
// will ever spend on one attachment). DecodeConfig reads only the
// header, so a highly compressed bomb — a tiny file claiming
// 50000×50000 — could otherwise force a multi-gigabyte allocation in
// image.Decode before the resize even starts. Over-cap images return
// unchanged, per this file's best-effort contract: the provider's own
// oversize rejection is clearer than anything we could synthesise here.
const maxInputPixels = 64 * 1000 * 1000

// anthShrinkImageBytesIfTooBig returns data unchanged when the image
// already fits within Anthropic's per-image dimension cap. When it
// doesn't, the image is decoded, resampled with Catmull-Rom (a good
// balance of quality and speed for downscaling), and re-encoded in
// the same format. The MIME type may be rewritten when re-encoding
// requires a format change (e.g. an image originally tagged image/gif
// is shipped as image/png after resize, since stdlib only encodes
// gif at the package's own narrow API which is awkward for a single
// frame).
//
// Errors during decode/encode return the original bytes untouched so
// the caller's existing flow continues to work; Anthropic will then
// emit its own clearer error than we could synthesise. We log no
// failures here because this runs on every outbound request and is
// best-effort.
func anthShrinkImageBytesIfTooBig(data []byte, mime string) ([]byte, string) {
	if len(data) == 0 {
		return data, mime
	}
	// Reconcile the declared media type with the actual bytes before
	// anything else. Callers can mislabel images (a .png that is really
	// JPEG, an extension that hardcodes a type), and so can already-
	// persisted session transcripts created before this fix existed.
	// Anthropic rejects the whole request when the declared type
	// disagrees with the bytes it sniffs, which would make such a session
	// impossible to continue. Sniffing the magic bytes here (independent
	// of stdlib decoder registration, and resilient to a DecodeConfig
	// failure below) makes the mismatch impossible to ship and lets a
	// previously-broken session resume cleanly.
	if real := anthSniffImageMIME(data); real != "" {
		mime = real
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return data, mime
	}
	if cfg.Width <= anthMaxImageSide && cfg.Height <= anthMaxImageSide {
		return data, mime
	}
	// Area check in int64: the per-axis values from a decoder fit in 32
	// bits, but their product does not have to fit in an int on every
	// platform.
	if int64(cfg.Width)*int64(cfg.Height) > maxInputPixels {
		return data, mime
	}
	// Compute target dimensions preserving aspect ratio, longest side
	// clamped to anthMaxImageSide.
	tw, th := cfg.Width, cfg.Height
	if tw >= th {
		th = th * anthMaxImageSide / tw
		tw = anthMaxImageSide
	} else {
		tw = tw * anthMaxImageSide / th
		th = anthMaxImageSide
	}
	if tw < 1 {
		tw = 1
	}
	if th < 1 {
		th = 1
	}

	src, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return data, mime
	}
	dst := image.NewRGBA(image.Rect(0, 0, tw, th))
	xdraw.CatmullRom.Scale(dst, dst.Bounds(), src, src.Bounds(), xdraw.Over, nil)

	var buf bytes.Buffer
	switch strings.ToLower(format) {
	case "jpeg":
		if err := jpeg.Encode(&buf, dst, &jpeg.Options{Quality: 85}); err != nil {
			return data, mime
		}
		return buf.Bytes(), "image/jpeg"
	case "png", "gif":
		// gifs are re-encoded as png so the call site stays simple
		// (avoiding a single-frame Encode helper). Anthropic accepts
		// image/png happily; the visual result is the same for the
		// model's vision pipeline.
		if err := png.Encode(&buf, dst); err != nil {
			return data, mime
		}
		nextMime := mime
		if strings.ToLower(format) == "gif" {
			nextMime = "image/png"
		}
		return buf.Bytes(), nextMime
	default:
		return data, mime
	}
}
