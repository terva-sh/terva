//go:build linux

package tui

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

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
	case IsWSL():
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
	case IsWSL():
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

const maxNativeClipboardImageBytes = 32 << 20

// runClipboardImageCmd reads PNG bytes without hiding backend failures.
func runClipboardImageCmd(name string, args ...string) ([]byte, bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	return runClipboardImageCmdContext(ctx, name, args...)
}

func runClipboardImageCmdContext(ctx context.Context, name string, args ...string) ([]byte, bool, error) {
	if _, err := exec.LookPath(name); err != nil {
		return nil, false, i18n.Errorf("%s not found — install it to paste clipboard images", name)
	}
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.WaitDelay = 100 * time.Millisecond
	cmd.Env = append(cmd.Environ(), "LC_ALL=C")
	stdout := clipboardCapture{limit: maxNativeClipboardImageBytes}
	stderr := clipboardCapture{limit: 4096}
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	if ctx.Err() != nil {
		return nil, false, fmt.Errorf("%s: %w", name, ctx.Err())
	}
	if stdout.overflow {
		return nil, false, i18n.Errorf("%s clipboard image exceeds %d bytes", name, maxNativeClipboardImageBytes)
	}
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 && !stderr.overflow && nativeClipboardHasNoImage(name, stderr.buf.String()) {
			return nil, false, nil
		}
		if diagnostic := clipSnippet(stderr.buf.String()); diagnostic != "" {
			return nil, false, fmt.Errorf("%s: %s: %w", name, diagnostic, err)
		}
		return nil, false, fmt.Errorf("%s: %w", name, err)
	}
	if stdout.buf.Len() == 0 {
		return nil, false, nil
	}
	return stdout.buf.Bytes(), true, nil
}

// The capture drains excess bytes so the child cannot block on a full pipe.
type clipboardCapture struct {
	buf      bytes.Buffer
	limit    int
	overflow bool
}

func (b *clipboardCapture) Write(p []byte) (int, error) {
	n := len(p)
	keep := min(n, b.limit-b.buf.Len())
	_, _ = b.buf.Write(p[:keep])
	b.overflow = b.overflow || keep < n
	return n, nil
}

func nativeClipboardHasNoImage(name, diagnostic string) bool {
	diagnostic = strings.Join(strings.Fields(diagnostic), " ")
	switch name {
	case "wl-paste":
		return diagnostic == "Nothing is copied" || diagnostic == `Clipboard content is not available as requested type "image/png" Use "wl-paste --list-types" to view available types.`
	case "xclip":
		return diagnostic == "Error: target image/png not available"
	}
	return false
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
