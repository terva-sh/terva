//go:build linux

package tui

import (
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// ReadClipboardImagePNG returns the system clipboard's image as PNG bytes.
// ok is false with a nil error when the clipboard holds no image; a non-nil
// error means the backend tool is missing or failed in a way worth showing.
// The backend is chosen by environment:
//
//   - WSL has no native Linux clipboard, so read Windows' clipboard via
//     powershell.exe (base64 across the interop pipe).
//   - Wayland: wl-paste (wl-clipboard).
//   - X11: xclip.
func ReadClipboardImagePNG() ([]byte, bool, error) {
	switch {
	case isWSL():
		return readClipboardImagePNGWSL()
	case os.Getenv("WAYLAND_DISPLAY") != "":
		return runClipboardImageCmd("wl-paste", "--no-newline", "--type", "image/png")
	case os.Getenv("DISPLAY") != "":
		return runClipboardImageCmd("xclip", "-selection", "clipboard", "-t", "image/png", "-o")
	default:
		return nil, false, fmt.Errorf("no clipboard backend detected (need WSL interop, Wayland, or X11)")
	}
}

// isWSL reports whether we're under the Windows Subsystem for Linux, where
// the only clipboard is Windows' own (reached via powershell.exe).
func isWSL() bool {
	if os.Getenv("WSL_INTEROP") != "" || os.Getenv("WSL_DISTRO_NAME") != "" {
		return true
	}
	b, _ := os.ReadFile("/proc/sys/kernel/osrelease")
	return strings.Contains(strings.ToLower(string(b)), "microsoft")
}

// runClipboardImageCmd runs a native-Linux clipboard reader that writes raw
// PNG bytes to stdout. A missing binary is an actionable error; any other
// non-zero exit means "no image of that type on the clipboard" (wl-paste and
// xclip both exit non-zero when the requested target isn't available).
func runClipboardImageCmd(name string, args ...string) ([]byte, bool, error) {
	if _, err := exec.LookPath(name); err != nil {
		return nil, false, fmt.Errorf("%s not found — install it to paste clipboard images", name)
	}
	out, err := exec.Command(name, args...).Output()
	if err != nil || len(out) == 0 {
		return nil, false, nil
	}
	return out, true, nil
}

// readClipboardImagePNGWSL pulls an image off the Windows clipboard from
// inside WSL. PowerShell re-encodes it to PNG and base64s it so the bytes
// survive the text-oriented interop pipe; -STA is required for clipboard
// access from PowerShell.
func readClipboardImagePNGWSL() ([]byte, bool, error) {
	const script = `Add-Type -AssemblyName System.Windows.Forms,System.Drawing; ` +
		`$img=[System.Windows.Forms.Clipboard]::GetImage(); ` +
		`if($img){ $ms=New-Object System.IO.MemoryStream; ` +
		`$img.Save($ms,[System.Drawing.Imaging.ImageFormat]::Png); ` +
		`[Convert]::ToBase64String($ms.ToArray()) }`
	if _, err := exec.LookPath("powershell.exe"); err != nil {
		return nil, false, fmt.Errorf("powershell.exe not on PATH (WSL interop disabled?)")
	}
	out, err := exec.Command("powershell.exe", "-NoProfile", "-STA", "-Command", script).Output()
	if err != nil {
		return nil, false, nil
	}
	s := strings.TrimSpace(string(out))
	if s == "" {
		return nil, false, nil
	}
	data, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return nil, false, fmt.Errorf("decode Windows clipboard image: %w", err)
	}
	return data, true, nil
}
