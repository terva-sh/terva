//go:build linux

package tui

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"terva.sh/terva/packages/testsupport"
)

// b64 of the bytes the reader script would carry for a tiny image.
var (
	wantBytes = []byte("\x89PNG\r\n\x1a\nfake-image-body")
	wantB64   = base64.StdEncoding.EncodeToString(wantBytes)
)

func TestParseClipboardImageOutput(t *testing.T) {
	cases := []struct {
		name   string
		stdout string
		want   []byte
		wantOK bool
		errHas string
	}{
		{name: "sentinel", stdout: "NO_IMAGE\n"},
		{name: "empty", stdout: ""},
		{name: "whitespace only", stdout: "  \n\t\n"},
		{name: "sentinel with noise before it", stdout: "some banner\nNO_IMAGE\n"},

		{name: "native png", stdout: "PNG:" + wantB64 + "\n", want: wantBytes, wantOK: true},
		{name: "dib fallback", stdout: "DIB:" + wantB64 + "\n", want: wantBytes, wantOK: true},
		{name: "crlf", stdout: "PNG:" + wantB64 + "\r\n", want: wantBytes, wantOK: true},

		// A banner before the payload must not be mistaken for it.
		{name: "banner then payload", stdout: "loading helper\nPNG:" + wantB64 + "\n", want: wantBytes, wantOK: true},

		// PowerShell does not wrap a plain string to a pipe, but if a host
		// ever did, the paste must keep working rather than fail to decode.
		{
			name:   "payload wrapped across lines",
			stdout: "PNG:" + wantB64[:8] + "\n" + wantB64[8:20] + "\n" + wantB64[20:] + "\n",
			want:   wantBytes, wantOK: true,
		},

		// The whole point of the fix: unrecognised output is an error, not a
		// silent "your clipboard is empty".
		{name: "garbage", stdout: "Something went sideways\n", errHas: "unexpected clipboard reader output"},
		{name: "unknown kind", stdout: "JPEG:" + wantB64 + "\n", errHas: "unexpected clipboard reader output"},
		{name: "undecodable payload", stdout: "PNG:not-valid-base64!!\n", errHas: "decode Windows clipboard image"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			data, ok, err := parseClipboardImageOutput(tc.stdout)
			if tc.errHas != "" {
				if err == nil {
					t.Fatalf("want an error containing %q, got ok=%v data=%q and no error", tc.errHas, ok, data)
				}
				if !strings.Contains(err.Error(), tc.errHas) {
					t.Fatalf("error = %q, want it to contain %q", err, tc.errHas)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tc.wantOK)
			}
			if string(data) != string(tc.want) {
				t.Fatalf("data = %q, want %q", data, tc.want)
			}
		})
	}
}

// A garbage line of any size must not become the status line verbatim.
func TestClipSnippetFoldsAndCaps(t *testing.T) {
	got := clipSnippet("first line\n   second   line\t\tthird\n")
	if got != "first line second line third" {
		t.Fatalf("clipSnippet = %q, want the message folded onto one line", got)
	}
	long := clipSnippet(strings.Repeat("x", 500))
	if len(long) > 130 {
		t.Fatalf("clipSnippet returned %d chars, want it capped near 120", len(long))
	}
	if !strings.HasSuffix(long, "...") {
		t.Fatalf("a capped snippet should say it was cut, got %q", long[len(long)-10:])
	}
}

// fakePowerShell puts an executable named powershell.exe at the front of PATH.
// body is the shell script that runs in its place, so the exec wrapper can be
// driven on any Linux box, CI included, with no Windows anywhere.
func fakePowerShell(t *testing.T, body string) {
	t.Helper()
	dir := testsupport.TempDir(t)
	path := filepath.Join(dir, "powershell.exe")
	script := "#!/bin/sh\n" + body + "\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake powershell.exe: %v", err)
	}
	t.Setenv("PATH", dir)
}

// The bug: every PowerShell failure returned ok=false with a nil error, so
// the caller told the user their clipboard held no image when interop was
// broken, an assembly was missing, or access was denied.
func TestWSLReaderSurfacesPowerShellFailure(t *testing.T) {
	fakePowerShell(t, `echo "Clipboard access denied." >&2; exit 1`)

	data, ok, err := readClipboardImagePNGWSL()
	if err == nil {
		t.Fatalf("a failing powershell.exe returned no error (ok=%v data=%d bytes); the failure is being swallowed as an empty clipboard", ok, len(data))
	}
	if !strings.Contains(err.Error(), "Clipboard access denied") {
		t.Fatalf("error = %q, want it to name the stderr cause", err)
	}
	if ok {
		t.Error("ok should be false when the reader failed")
	}
}

// A failure with nothing on stderr must still be an error, not a silent miss.
func TestWSLReaderReportsSilentFailure(t *testing.T) {
	fakePowerShell(t, `exit 3`)

	if _, ok, err := readClipboardImagePNGWSL(); err == nil {
		t.Fatalf("a non-zero exit with empty stderr returned no error (ok=%v)", ok)
	}
}

// An empty clipboard is the one case that legitimately reports "no image".
func TestWSLReaderEmptyClipboardIsNotAnError(t *testing.T) {
	fakePowerShell(t, `echo NO_IMAGE`)

	data, ok, err := readClipboardImagePNGWSL()
	if err != nil {
		t.Fatalf("an empty clipboard must not be an error, got %v", err)
	}
	if ok || len(data) != 0 {
		t.Fatalf("ok=%v data=%d bytes, want no image", ok, len(data))
	}
}

func TestWSLReaderReturnsImageBytes(t *testing.T) {
	fakePowerShell(t, `echo "PNG:`+wantB64+`"`)

	data, ok, err := readClipboardImagePNGWSL()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok {
		t.Fatal("ok = false, want the image to be returned")
	}
	if string(data) != string(wantBytes) {
		t.Fatalf("data = %q, want %q", data, wantBytes)
	}
}

// The script must prefer the clipboard's own PNG over GetImage(), whose GDI+
// re-encode flattens the alpha channel. Measured on WSL2: a transparent pixel
// comes back opaque grey (r=211 g=211 b=211 a=255) through GetImage, and
// byte-identical through the PNG format.
func TestReaderScriptPrefersNativePNG(t *testing.T) {
	png := strings.Index(readClipboardImageScript, "ContainsData('PNG')")
	get := strings.Index(readClipboardImageScript, "GetImage()")
	if png < 0 {
		t.Fatal("the reader script no longer checks for the native PNG clipboard format")
	}
	if get < 0 {
		t.Fatal("the reader script no longer falls back to GetImage; Print Screen offers CF_DIB only")
	}
	if png > get {
		t.Error("GetImage is tried before the native PNG format, which re-encodes and drops transparency")
	}
}
