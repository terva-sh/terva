//go:build darwin

package tui

import (
	"bytes"
	"fmt"
	"image/png"
	"os"
	"os/exec"
	"strings"

	"golang.org/x/image/tiff"
)

// readClipboardImageScript dumps the clipboard image to argv[1] and prints
// its kind ("png", else "tiff") or "NO_IMAGE". Using osascript avoids a
// cgo/AppKit dependency, which keeps the terva binary static.
const readClipboardImageScript = `
on run argv
	set outPath to item 1 of argv
	try
		set imgData to the clipboard as «class PNGf»
		set imgKind to "png"
	on error
		try
			set imgData to the clipboard as «class TIFF»
			set imgKind to "tiff"
		on error
			return "NO_IMAGE"
		end try
	end try
	set fileRef to open for access (POSIX file outPath) with write permission
	try
		set eof of fileRef to 0
		write imgData to fileRef
		close access fileRef
	on error errMsg number errNum
		try
			close access fileRef
		end try
		error errMsg number errNum
	end try
	return imgKind
end run
`

// ReadClipboardImagePNG returns the system clipboard's image as PNG bytes.
// ok is false with a nil error when the clipboard holds no image; a non-nil
// error means osascript itself failed. macOS hands images out as PNG or
// TIFF, so a TIFF is decoded and re-encoded to PNG.
func ReadClipboardImagePNG() ([]byte, bool, error) {
	tmp, err := os.CreateTemp("", "terva-clipboard-*.img")
	if err != nil {
		return nil, false, err
	}
	path := tmp.Name()
	_ = tmp.Close()
	defer os.Remove(path)

	out, err := exec.Command("/usr/bin/osascript", "-e", readClipboardImageScript, path).CombinedOutput()
	kind := strings.TrimSpace(string(out))
	if err != nil {
		// AppleScript phrases "no image on the clipboard" a few ways; all
		// mean ok=false rather than a real failure.
		if strings.Contains(kind, "NO_IMAGE") || strings.Contains(kind, "Can’t make") || strings.Contains(kind, "Can't make") {
			return nil, false, nil
		}
		if kind == "" {
			return nil, false, fmt.Errorf("osascript failed: %w", err)
		}
		return nil, false, fmt.Errorf("osascript failed: %s", kind)
	}
	if kind == "NO_IMAGE" {
		return nil, false, nil
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, false, err
	}
	switch kind {
	case "png":
		return raw, true, nil
	case "tiff":
		img, err := tiff.Decode(bytes.NewReader(raw))
		if err != nil {
			return nil, false, fmt.Errorf("decode clipboard TIFF: %w", err)
		}
		var buf bytes.Buffer
		if err := png.Encode(&buf, img); err != nil {
			return nil, false, fmt.Errorf("encode clipboard PNG: %w", err)
		}
		return buf.Bytes(), true, nil
	default:
		return nil, false, fmt.Errorf("unexpected clipboard image kind %q", kind)
	}
}
