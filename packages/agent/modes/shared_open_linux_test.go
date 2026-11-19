//go:build linux

package modes

// openInSystemViewer under WSL.
//
// A WSL distribution is Linux to the Go runtime and has Windows for a desktop,
// so xdg-open is usually missing and the `o` key in /shared failed there. These
// tests run the real function against stand-in programs on PATH and read what
// the opener was handed. Asserting on a command line this file built itself
// would prove only that the test can build one.
//
// Linux only: the stand-ins are shell scripts, and the branch under test does
// not compile into the other platforms' switch arms.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"terva.sh/terva/packages/testsupport"
)

// fakeBin writes an executable shell script named name into dir.
func fakeBin(t *testing.T, dir, name, body string) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
}

// openerSandbox is a PATH holding only this test's stand-ins. Each opener
// appends "<its name> <its first argument>" to the returned record file, so one
// file answers both which program ran and what it was given.
func openerSandbox(t *testing.T, bins ...string) (dir, record string) {
	t.Helper()
	dir = testsupport.TempDir(t)
	record = filepath.Join(dir, "opened")
	for _, name := range bins {
		switch name {
		case "wslpath":
			// The real tool prints the Windows form of its last argument. This
			// one marks the conversion, so a path that skipped it is visible.
			fakeBin(t, dir, name, `[ "$1" = "-w" ] || exit 2`+"\n"+`printf 'WIN[%s]\n' "$2"`)
		default:
			fakeBin(t, dir, name, `printf '%s %s\n' "`+name+`" "$1" >> "`+record+`"`)
		}
	}
	// Only the sandbox: a real xdg-open on the developer's PATH must not be
	// able to answer for a stand-in that was left out on purpose.
	t.Setenv("PATH", dir)
	return dir, record
}

// swapWSL pins the WSL answer for one test.
func swapWSL(t *testing.T, wsl bool) {
	t.Helper()
	prev := underWSL
	underWSL = func() bool { return wsl }
	t.Cleanup(func() { underWSL = prev })
}

// waitForRecord reads what the opener wrote. The opener is started and not
// waited on, as a real viewer must be, so the test waits on its output.
func waitForRecord(t *testing.T, record string) string {
	t.Helper()
	for range 400 {
		if b, err := os.ReadFile(record); err == nil && strings.HasSuffix(string(b), "\n") {
			return strings.TrimSpace(string(b))
		}
		<-time.After(5 * time.Millisecond)
	}
	t.Fatalf("no opener ran: %s was never written", record)
	return ""
}

const openedPath = "/home/someone/.terva/shared/s1/shr_a-chart.png"

// The fix: under WSL the file goes to Windows, as a path Windows can read.
func TestOpenUnderWSLHandsTheWindowsPathToExplorer(t *testing.T) {
	_, record := openerSandbox(t, "wslpath", "explorer.exe", "xdg-open")
	swapWSL(t, true)

	if err := openInSystemViewer(openedPath); err != nil {
		t.Fatalf("openInSystemViewer: %v", err)
	}

	if got, want := waitForRecord(t, record), "explorer.exe WIN["+openedPath+"]"; got != want {
		t.Errorf("opener ran as %q, want %q", got, want)
	}
}

// The positive control for the test above. With the same programs on PATH and
// WSL off, the Linux opener runs with the Linux path. Without this, a sandbox
// whose explorer.exe always won would pass the WSL test for the wrong reason.
func TestOpenOnPlainLinuxUsesXdgOpen(t *testing.T) {
	_, record := openerSandbox(t, "wslpath", "explorer.exe", "xdg-open")
	swapWSL(t, false)

	if err := openInSystemViewer(openedPath); err != nil {
		t.Fatalf("openInSystemViewer: %v", err)
	}

	if got, want := waitForRecord(t, record), "xdg-open "+openedPath; got != want {
		t.Errorf("opener ran as %q, want %q", got, want)
	}
}

// Interop can be off, and `appendWindowsPath = false` leaves explorer.exe off
// PATH. A WSL user in either state may have given the distribution a Linux
// desktop, so the Linux opener is the fallback and not an error.
func TestOpenUnderWSLWithoutExplorerFallsBackToXdgOpen(t *testing.T) {
	_, record := openerSandbox(t, "wslpath", "xdg-open")
	swapWSL(t, true)

	if err := openInSystemViewer(openedPath); err != nil {
		t.Fatalf("openInSystemViewer: %v", err)
	}

	if got, want := waitForRecord(t, record), "xdg-open "+openedPath; got != want {
		t.Errorf("opener ran as %q, want %q", got, want)
	}
}

// 🚨 Interop switched off is not the same state as explorer.exe missing, and the
// test above covers only the second. With interop off the Windows directories
// are still on PATH, so explorer.exe is found and wslpath still converts. What
// fails is the exec itself: the kernel has no handler for a Windows program
// and answers with an exec format error. The stand-in here is that file, a
// program that is discoverable and cannot run.
//
// Before the opener tried the Windows route instead of choosing it, this case
// returned the exec error and never reached xdg-open. PR #1738's first review
// found it.
func TestOpenUnderWSLWithExplorerThatCannotRunFallsBackToXdgOpen(t *testing.T) {
	dir, record := openerSandbox(t, "wslpath", "xdg-open")
	// The first bytes of a Windows executable, and no shebang: exec refuses it.
	notRunnable := filepath.Join(dir, "explorer.exe")
	if err := os.WriteFile(notRunnable, []byte("MZ\x90\x00 a Windows program, which this kernel cannot run\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	swapWSL(t, true)

	if err := openInSystemViewer(openedPath); err != nil {
		t.Fatalf("openInSystemViewer: %v", err)
	}

	if got, want := waitForRecord(t, record), "xdg-open "+openedPath; got != want {
		t.Errorf("opener ran as %q, want %q", got, want)
	}
}

// A wslpath that fails must not hand Windows an unconverted Linux path, which
// explorer.exe answers by opening the Documents folder instead of an error.
func TestOpenUnderWSLWithAFailingWslpathFallsBackToXdgOpen(t *testing.T) {
	dir, record := openerSandbox(t, "explorer.exe", "xdg-open")
	fakeBin(t, dir, "wslpath", "exit 1")
	swapWSL(t, true)

	if err := openInSystemViewer(openedPath); err != nil {
		t.Fatalf("openInSystemViewer: %v", err)
	}

	if got, want := waitForRecord(t, record), "xdg-open "+openedPath; got != want {
		t.Errorf("opener ran as %q, want %q", got, want)
	}
}

// With no opener at all the failure reaches the caller, which reports it in the
// panel. A silent success here is a key that looks like it did nothing.
func TestOpenWithNoOpenerReportsTheFailure(t *testing.T) {
	openerSandbox(t)
	swapWSL(t, true)

	if err := openInSystemViewer(openedPath); err == nil {
		t.Error("openInSystemViewer reported success with nothing on PATH to open a file")
	}
}
