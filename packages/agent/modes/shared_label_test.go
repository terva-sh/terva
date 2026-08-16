package modes

// The two /shared notices that interpolate a filename into a sentence.
//
// "opened %s" takes the name straight from the LISTING, which on `terva attach`
// came off a daemon this client does not control. "saved %s" takes the name of
// the file just written, and sanitizeSavedName — the thing that produced it —
// deliberately lets a control byte through: a filename may legally contain one,
// and refusing to save is worse than saving it. What is legal on disk is not
// therefore safe to paint, so the notice sanitizes separately.
//
// Escape bytes are counted against a benign control render. The panel's own
// styling is present in both, so any surplus came from the name.

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/testsupport"
	"terva.sh/terva/packages/tui"
)

// rawNotice renders the panel with its styling intact, unlike dialogNotice.
func rawNotice(i *Interactive) string {
	return strings.Join(i.sharedDialog.Render(tui.Dark, 80), "\n")
}

// The name in the listing reaches the "opened" notice, so it is sanitized there.
func TestOpenedNoticeNeutralisesAHostileName(t *testing.T) {
	opened := func(name string) *Interactive {
		c := newShareCarrier(nil)
		i := sharedActionInteractive(t, c, testsupport.TempDir(t), true, []ctrlproto.SharedFileEntry{
			entry("shr_a", name, "/daemon/side/shr_a-report.pdf"),
		})
		defer swapViewer(func(string) error { return nil })()
		i.openSharedFile("shr_a")
		return i
	}

	benign := rawNotice(opened("report.pdf"))
	hostile := rawNotice(opened("report\x1b[31m\x1b[2J\x1b]0;pwned\x07.pdf"))

	if !strings.Contains(stripANSICodes(benign), "opened") {
		t.Fatalf("the fixture did not produce an opened notice:\n%s", benign)
	}
	if got, want := strings.Count(hostile, "\x1b"), strings.Count(benign, "\x1b"); got != want {
		t.Errorf("the name contributed %d escape bytes to the notice", got-want)
	}
	if strings.Contains(hostile, "pwned") {
		t.Errorf("the OSC payload reached the notice:\n%s", hostile)
	}
	if !strings.Contains(stripANSICodes(hostile), "report.pdf") {
		t.Errorf("the readable part of the name should survive:\n%s", stripANSICodes(hostile))
	}
}

// The saved name is chosen by terva, so it is printable before it is written —
// and the notice sanitizes anyway, because the two decisions are separate.
func TestSavedNoticeNeutralisesAHostileName(t *testing.T) {
	saved := func(name string) (*Interactive, string) {
		cwd := testsupport.TempDir(t)
		c := newShareCarrier(map[string][]byte{"shr_a": []byte("BODY")})
		c.name = name
		i := sharedActionInteractive(t, c, cwd, true, []ctrlproto.SharedFileEntry{
			entry("shr_a", "report.pdf", "/daemon/side/shr_a-report.pdf"),
		})
		i.saveSharedFile("shr_a")
		return i, cwd
	}

	benignI, _ := saved("report.pdf")
	hostileI, cwd := saved("report\x1b[31m\x1b[2J\x1b]0;pwned\x07.pdf")
	benign, hostile := rawNotice(benignI), rawNotice(hostileI)

	if !strings.Contains(stripANSICodes(benign), "saved") {
		t.Fatalf("the fixture did not produce a saved notice:\n%s", benign)
	}
	if got, want := strings.Count(hostile, "\x1b"), strings.Count(benign, "\x1b"); got != want {
		t.Errorf("the saved name contributed %d escape bytes to the notice", got-want)
	}
	if strings.Contains(hostile, "pwned") {
		t.Errorf("the OSC payload reached the notice:\n%s", hostile)
	}

	// The save itself must still have happened: sanitizing the NOTICE is not
	// licence to refuse the file, and the bytes are what the user asked for.
	entries, err := os.ReadDir(cwd)
	if err != nil || len(entries) != 1 {
		t.Fatalf("expected exactly one saved file, got %v (err %v)", entries, err)
	}
	body, err := os.ReadFile(filepath.Join(cwd, entries[0].Name()))
	if err != nil || string(body) != "BODY" {
		t.Errorf("the saved copy is wrong: %q (err %v)", body, err)
	}

	// And the name on DISK is printable. Windows rejects a control byte in a
	// filename outright, so leaving one in did not preserve the save there, it
	// prevented it — the create failed and the user got nothing. Everywhere
	// else the name still reaches a terminal the moment anything lists the
	// directory, which is a surface that never opted in.
	if n := entries[0].Name(); strings.ContainsFunc(n, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
		t.Errorf("a control byte reached the filename on disk: %q", n)
	}
}

// The FAILURE notices print the error verbatim, and on `terva attach` that text
// came off a daemon this client does not control. Sanitizing the success notice
// and not the failure one leaves the guard above reachable by making the save
// fail — which is not an exotic condition: a full disk, a read-only directory
// or a refused permission gets there on any platform.
//
// The error is the carrier here rather than the filename, because the filename
// no longer reaches this path: sanitizeSavedName cleans it before the create is
// attempted. A test that smuggled the name in would pass without asserting
// anything about the notice.
func TestFailedSaveNoticeNeutralisesAHostileError(t *testing.T) {
	failed := func(msg string) *Interactive {
		c := newShareCarrier(map[string][]byte{"shr_a": []byte("BODY")})
		c.err = errors.New(msg)
		i := sharedActionInteractive(t, c, testsupport.TempDir(t), true, []ctrlproto.SharedFileEntry{
			entry("shr_a", "report.pdf", "/daemon/side/shr_a-report.pdf"),
		})
		i.saveSharedFile("shr_a")
		return i
	}

	benign := rawNotice(failed("disk full"))
	hostile := rawNotice(failed("disk\x1b[31m\x1b[2J\x1b]0;pwned\x07full"))

	// The fixture has to actually fail, or this asserts nothing at all.
	if !strings.Contains(stripANSICodes(benign), "save failed") {
		t.Fatalf("the fixture did not produce a failure notice:\n%s", stripANSICodes(benign))
	}
	if got, want := strings.Count(hostile, "\x1b"), strings.Count(benign, "\x1b"); got != want {
		t.Errorf("the error text contributed %d escape bytes to the notice", got-want)
	}
	if strings.Contains(hostile, "pwned") {
		t.Errorf("the OSC payload reached the failure notice:\n%s", hostile)
	}
}
