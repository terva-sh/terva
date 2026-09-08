//go:build linux

package tui

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"terva.sh/terva/packages/i18n"
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
		return nil, false, i18n.Errorf("no clipboard backend detected (need WSL interop, Wayland, or X11)")
	}
}

// WriteClipboardText puts s on the system clipboard.
//
// Same backend selection as the image read above, pointed the other way, and
// the same reason each branch exists: WSL has no native Linux clipboard, so it
// goes to Windows' own through interop; Wayland and X11 have their own tools.
// A missing backend is a real error rather than a silent success — a "copied"
// message for a clipboard that never received anything is worse than saying so.
func WriteClipboardText(s string) error {
	switch {
	case isWSL():
		return runClipboardWrite(s, "clip.exe")
	case os.Getenv("WAYLAND_DISPLAY") != "":
		return runClipboardWrite(s, "wl-copy")
	case os.Getenv("DISPLAY") != "":
		return runClipboardWrite(s, "xclip", "-selection", "clipboard")
	default:
		return i18n.Errorf("no clipboard backend detected (need WSL interop, Wayland, or X11)")
	}
}

// runClipboardWrite feeds s to a clipboard tool's stdin.
func runClipboardWrite(s, name string, args ...string) error {
	if _, err := exec.LookPath(name); err != nil {
		return i18n.Errorf("%s not found — install it to copy to the clipboard", name)
	}
	cmd := exec.Command(name, args...)
	cmd.Stdin = strings.NewReader(s)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	return nil
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
		return nil, false, i18n.Errorf("%s not found — install it to paste clipboard images", name)
	}
	out, err := exec.Command(name, args...).Output()
	if err != nil || len(out) == 0 {
		return nil, false, nil
	}
	return out, true, nil
}

// clipboardNoImageSentinel is what the reader script prints when the Windows
// clipboard holds no image at all.
//
// 🪤 This is PARSED, not displayed, so it stays English. Translate it and an
// empty clipboard stops reading as empty: it becomes a spurious error on
// every paste instead.
const clipboardNoImageSentinel = "NO_IMAGE"

// readClipboardImageScript reads the Windows clipboard and prints one line:
// the NO_IMAGE sentinel, or "<kind>:<base64 PNG>".
//
// It prefers the clipboard's own PNG format over GetImage(). GetImage hands
// the DIB to GDI+ and re-encodes it, and that round trip drops the alpha
// channel, so a browser "Copy image" on a transparent PNG comes back with
// black where the transparency was. A source that offers PNG (the Snipping
// Tool and every browser do) is offering the original file, which needs no
// re-encode. Print Screen offers CF_DIB only, so GetImage stays as the
// fallback rather than being removed.
//
// The kind is reported so a failure can say which path produced the bytes.
// Both kinds are PNG by the time they reach Go.
//
// -STA is required for clipboard access from PowerShell. ErrorActionPreference
// plus the catch turn a non-terminating PowerShell error into a non-zero exit
// with the cause on stderr, rather than an empty stdout that reads as an
// empty clipboard.
const readClipboardImageScript = `
$ErrorActionPreference = 'Stop'
try {
	Add-Type -AssemblyName System.Windows.Forms, System.Drawing
	$bytes = $null
	$kind = ''
	if ([System.Windows.Forms.Clipboard]::ContainsData('PNG')) {
		$data = [System.Windows.Forms.Clipboard]::GetData('PNG')
		if ($data -is [System.IO.MemoryStream]) {
			$bytes = $data.ToArray()
			$kind = 'PNG'
		}
	}
	if (-not $bytes) {
		$img = [System.Windows.Forms.Clipboard]::GetImage()
		if ($img) {
			$ms = New-Object System.IO.MemoryStream
			$img.Save($ms, [System.Drawing.Imaging.ImageFormat]::Png)
			$bytes = $ms.ToArray()
			$kind = 'DIB'
		}
	}
	if ($bytes) { $kind + ':' + [Convert]::ToBase64String($bytes) } else { 'NO_IMAGE' }
} catch {
	[Console]::Error.WriteLine($_.Exception.Message)
	exit 1
}
`

// lastClipboardLine returns the last non-empty line of the reader's stdout.
// A PowerShell banner or a helper loaded into the process can print before
// the payload, so the sentinel is looked for on the last line rather than in
// the whole buffer. The darwin reader takes the same precaution.
func lastClipboardLine(s string) string {
	var last string
	for _, line := range strings.Split(s, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			last = line
		}
	}
	return last
}

// cutClipboardPayload finds the last "<kind>:" marker and returns the kind
// with everything after it.
//
// Everything after it, rather than the rest of that one line: the payload is
// base64, and a host that wrapped it would otherwise lose every line but the
// last. Taking the last marker means a banner that happens to mention one
// cannot be mistaken for the payload. A colon never appears in standard
// base64, so no marker can be found inside the data itself.
func cutClipboardPayload(s string) (kind, payload string, ok bool) {
	best := -1
	for _, k := range []string{"PNG", "DIB"} {
		if i := strings.LastIndex(s, k+":"); i > best {
			best, kind = i, k
		}
	}
	if best < 0 {
		return "", "", false
	}
	return kind, s[best+len(kind)+1:], true
}

// stripBase64Whitespace removes the whitespace a wrapped payload would carry.
// PowerShell does not wrap a plain string written to a pipe, measured at 63838
// bytes on one line, so this is insurance: if a future host does wrap, the
// paste keeps working instead of failing to decode.
func stripBase64Whitespace(s string) string {
	return strings.Map(func(r rune) rune {
		switch r {
		case ' ', '\t', '\r', '\n':
			return -1
		}
		return r
	}, s)
}

// clipSnippet folds a message onto one line and caps it, so a megabyte of
// stray output cannot become the status line.
func clipSnippet(s string) string {
	const max = 120
	s = strings.Join(strings.Fields(s), " ")
	if len(s) <= max {
		return s
	}
	return strings.ToValidUTF8(s[:max], "") + "..."
}

// parseClipboardImageOutput turns the reader script's stdout into PNG bytes.
// ok is false with a nil error only for the sentinel and for empty output;
// anything unrecognised is an error, because reporting it as an empty
// clipboard is what made a broken reader undiagnosable.
func parseClipboardImageOutput(stdout string) ([]byte, bool, error) {
	s := strings.TrimSpace(stdout)
	if s == "" {
		return nil, false, nil
	}
	_, payload, found := cutClipboardPayload(s)
	if !found {
		if lastClipboardLine(s) == clipboardNoImageSentinel {
			return nil, false, nil
		}
		return nil, false, i18n.Errorf("unexpected clipboard reader output: %s", clipSnippet(s))
	}
	data, err := base64.StdEncoding.DecodeString(stripBase64Whitespace(payload))
	if err != nil {
		return nil, false, fmt.Errorf("decode Windows clipboard image: %w", err)
	}
	if len(data) == 0 {
		return nil, false, nil
	}
	return data, true, nil
}

// readClipboardImagePNGWSL pulls an image off the Windows clipboard from
// inside WSL, base64 across the interop pipe because it is text-oriented.
//
// A PowerShell failure is reported, not swallowed. It used to return
// "no image", so broken interop, a missing assembly and an empty clipboard
// were indistinguishable, and the caller told the user their clipboard was
// empty when it was not.
func readClipboardImagePNGWSL() ([]byte, bool, error) {
	if _, err := exec.LookPath("powershell.exe"); err != nil {
		return nil, false, i18n.Errorf("powershell.exe not on PATH (WSL interop disabled?)")
	}
	cmd := exec.Command("powershell.exe", "-NoProfile", "-STA", "-Command", readClipboardImageScript)

	// 🪤 Separate pipes, not CombinedOutput: PowerShell writes progress and
	// warning records to stderr while still exiting 0. Merged, that noise
	// lands in front of the payload and every paste fails to parse.
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if msg := clipSnippet(stderr.String()); msg != "" {
			return nil, false, i18n.Errorf("read the Windows clipboard: %s", msg)
		}
		return nil, false, fmt.Errorf("read the Windows clipboard: %w", err)
	}
	return parseClipboardImageOutput(stdout.String())
}
