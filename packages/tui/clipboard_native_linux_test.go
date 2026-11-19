//go:build linux

package tui

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"terva.sh/terva/packages/testsupport"
)

func fakeNativeClipboard(t *testing.T, name, body string) {
	t.Helper()
	dir := testsupport.TempDir(t)
	if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func TestNativeClipboardDiagnostics(t *testing.T) {
	for _, backend := range []string{"wl-paste", "xclip"} {
		t.Run(backend, func(t *testing.T) {
			for _, tc := range []struct {
				name, body, want, errHas string
			}{
				{"image", `printf 'PNG-bytes'`, "PNG-bytes", ""},
				{"empty output", `exit 0`, "", ""},
				{"image with warning", `printf 'warning\n' >&2; printf 'PNG-bytes'`, "PNG-bytes", ""},
				{"connection failure", `printf 'Cannot connect to display\n' >&2; exit 1`, "", "Cannot connect to display"},
				{"permission failure", `printf 'Permission denied\n' >&2; exit 1`, "", "Permission denied"},
				{"silent failure", `exit 2`, "", "exit status 2"},
				{"unexpected exit code", `printf 'Nothing is copied\n' >&2; exit 2`, "", "exit status 2"},
				{"mixed diagnostic", `printf 'Nothing is copied\nPermission denied\n' >&2; exit 1`, "", "Permission denied"},
				{"partial failed output", `printf 'partial image'; printf 'backend failed\n' >&2; exit 1`, "", "backend failed"},
			} {
				t.Run(tc.name, func(t *testing.T) {
					fakeNativeClipboard(t, backend, tc.body)
					data, ok, err := runClipboardImageCmd(backend)
					if tc.errHas != "" {
						if err == nil || !strings.Contains(err.Error(), tc.errHas) || ok || len(data) != 0 {
							t.Fatalf("data=%q ok=%v err=%v, want failure containing %q", data, ok, err, tc.errHas)
						}
					} else if err != nil || ok != (tc.want != "") || string(data) != tc.want {
						t.Fatalf("data=%q ok=%v err=%v, want %q", data, ok, err, tc.want)
					}
				})
			}
		})
	}
}

func TestNativeClipboardAbsentImage(t *testing.T) {
	for _, tc := range []struct{ backend, diagnostic string }{
		{"wl-paste", "Nothing is copied"},
		{"wl-paste", "Clipboard content is not available as requested type \"image/png\"\nUse \"wl-paste --list-types\" to view available types."},
		{"xclip", "Error: target image/png not available"},
	} {
		t.Run(tc.backend+"/"+tc.diagnostic, func(t *testing.T) {
			t.Setenv("FAKE_CLIPBOARD_DIAGNOSTIC", tc.diagnostic)
			fakeNativeClipboard(t, tc.backend, `printf '%s\n' "$FAKE_CLIPBOARD_DIAGNOSTIC" >&2; exit 1`)
			if data, ok, err := runClipboardImageCmd(tc.backend); err != nil || ok || len(data) != 0 {
				t.Fatalf("data=%q ok=%v err=%v, want no image", data, ok, err)
			}
		})
	}
}

func TestNativeClipboardDeadline(t *testing.T) {
	fakeNativeClipboard(t, "wl-paste", `while :; do :; done`)
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	started := time.Now()
	data, ok, err := runClipboardImageCmdContext(ctx, "wl-paste")
	if !errors.Is(err, context.DeadlineExceeded) || ok || len(data) != 0 {
		t.Fatalf("data=%q ok=%v err=%v, want deadline error", data, ok, err)
	}
	if time.Since(started) > 2*time.Second {
		t.Fatal("clipboard command did not stop after its deadline")
	}
}

func TestNativeClipboardOutputBounds(t *testing.T) {
	t.Run("image", func(t *testing.T) {
		cat, err := exec.LookPath("cat")
		if err != nil {
			t.Skip("cat is unavailable for the fake backend")
		}
		path := filepath.Join(testsupport.TempDir(t), "oversize-image")
		if err := os.WriteFile(path, make([]byte, maxNativeClipboardImageBytes+1), 0o600); err != nil {
			t.Fatal(err)
		}
		t.Setenv("CLIPBOARD_CAT", cat)
		t.Setenv("CLIPBOARD_IMAGE", path)
		fakeNativeClipboard(t, "xclip", `exec "$CLIPBOARD_CAT" "$CLIPBOARD_IMAGE"`)
		if data, ok, err := runClipboardImageCmd("xclip"); err == nil || !strings.Contains(err.Error(), "exceeds") || ok || len(data) != 0 {
			t.Fatalf("data=%d bytes ok=%v err=%v, want size limit", len(data), ok, err)
		}
	})
	t.Run("diagnostics", func(t *testing.T) {
		fakeNativeClipboard(t, "wl-paste", `i=0; while [ "$i" -lt 10000 ]; do printf x; i=$((i+1)); done >&2; exit 1`)
		if _, ok, err := runClipboardImageCmd("wl-paste"); err == nil || ok || len(err.Error()) > 200 || !strings.Contains(err.Error(), "...") {
			t.Fatalf("ok=%v err=%v, want bounded diagnostic", ok, err)
		}
	})
}

func TestNativeClipboardExecutionFailure(t *testing.T) {
	dir := testsupport.TempDir(t)
	path := filepath.Join(dir, "wl-paste")
	if err := os.WriteFile(path, []byte("#!"+filepath.Join(dir, "absent-interpreter")+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	if _, ok, err := runClipboardImageCmd("wl-paste"); err == nil || ok {
		t.Fatalf("ok=%v err=%v, want execution failure", ok, err)
	}
}
