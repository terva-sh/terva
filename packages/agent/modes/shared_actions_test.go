package modes

// The /shared panel's actions, which is where this feature touches things
// outside terva: the clipboard, a system viewer, and the user's own directory.
//
// Two properties carry the weight. A path from the listing names the DAEMON's
// disk, so copy and open are only meaningful when the daemon is this machine —
// on `terva attach` they must refuse and point at save, which is the verb that
// moves bytes. And save must never overwrite: it is a convenience, and a
// convenience that destroys the user's file is a bug they cannot undo.

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/agent/modes/dialogs"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/privfs"
	"terva.sh/terva/packages/testsupport"
	"terva.sh/terva/packages/tui"
)

// sharedActionInteractive is a TUI bound to sess with the /shared panel open
// over a fixed listing, running against cwd.
func sharedActionInteractive(t *testing.T, c *shareCarrier, cwd string, local bool, files []ctrlproto.SharedFileEntry) *Interactive {
	t.Helper()
	i := newCtrlprotoTestInteractive()
	i.cfg.Carrier = c
	i.cfg.CarrierSession = "s1"
	i.cfg.CarrierLocal = local
	i.cfg.CWD = cwd
	i.sharedDialog = dialogs.NewSharedDialog()
	i.sharedFiles = files
	i.sharedFilesSession = "s1"
	i.sharedDialog.Open(i.sharedFileRows)
	return i
}

// dialogNotice is the message the panel is currently showing.
func dialogNotice(i *Interactive) string {
	return stripANSI(i.sharedDialog.Render(tui.Dark, 80))
}

// swapClipboard and swapViewer install a stand-in for the two package vars that
// reach outside terva, and return the restore. Call as:
//
//	defer swapClipboard(fn)()
//
// Restoring matters more than it looks: these are package-level, so a test that
// left its stand-in behind would hand it to every test after it in the binary,
// and the failure would surface somewhere else entirely.
func swapClipboard(fn func(string) error) func() {
	prev := writeClipboard
	writeClipboard = fn
	return func() { writeClipboard = prev }
}

func swapViewer(fn func(string) error) func() {
	prev := openSystemViewer
	openSystemViewer = fn
	return func() { openSystemViewer = prev }
}

func entry(id, name string, path string) ctrlproto.SharedFileEntry {
	return ctrlproto.SharedFileEntry{
		SharedFile: core.SharedFile{ID: id, Name: name, Kind: "document", Size: 4},
		Path:       path,
	}
}

// Save is the verb that works everywhere, because it moves the bytes rather
// than pointing at them.
func TestSaveSharedFileWritesIntoTheWorkingDirectory(t *testing.T) {
	cwd := testsupport.TempDir(t)
	c := newShareCarrier(map[string][]byte{"shr_a": []byte("BODY")})
	i := sharedActionInteractive(t, c, cwd, true, []ctrlproto.SharedFileEntry{
		entry("shr_a", "chart.png", "/daemon/side/shr_a-chart.png"),
	})

	i.saveSharedFile("shr_a")

	got, err := os.ReadFile(filepath.Join(cwd, "chart.png"))
	if err != nil {
		t.Fatalf("the file was not saved: %v", err)
	}
	if string(got) != "BODY" {
		t.Errorf("saved %q, want the fetched bytes", got)
	}
	if notice := dialogNotice(i); !strings.Contains(notice, "saved chart.png") {
		t.Errorf("the panel does not report the save:\n%s", notice)
	}
}

// Never overwrite. The user's own file is not terva's to destroy to make room
// for a copy they asked for as a convenience.
func TestSaveSharedFileDoesNotOverwriteAnExistingFile(t *testing.T) {
	cwd := testsupport.TempDir(t)
	mine := filepath.Join(cwd, "chart.png")
	if err := os.WriteFile(mine, []byte("MY OWN WORK"), 0o644); err != nil {
		t.Fatal(err)
	}
	c := newShareCarrier(map[string][]byte{"shr_a": []byte("BODY")})
	i := sharedActionInteractive(t, c, cwd, true, []ctrlproto.SharedFileEntry{
		entry("shr_a", "chart.png", "/daemon/side/shr_a-chart.png"),
	})

	i.saveSharedFile("shr_a")

	if got, _ := os.ReadFile(mine); string(got) != "MY OWN WORK" {
		t.Fatalf("the existing file was overwritten: %q", got)
	}
	if got, err := os.ReadFile(filepath.Join(cwd, "chart-2.png")); err != nil || string(got) != "BODY" {
		t.Errorf("the copy did not land beside it: %q, %v", got, err)
	}
}

// The never-overwrite promise has to hold against a file that appears AFTER the
// name was chosen, which is the only case that actually destroys data.
//
// The old implementation stat'd for a free name and then wrote unconditionally.
// A stat answers a question about the past: between "is this name free" and
// "write it" the user can save their own work under that name, and the write
// then silently destroys it. The window is small and entirely real — two saves
// of the same share, or a file manager, land in it.
//
// A DANGLING symlink makes the same flaw deterministic rather than a race to
// lose, and it is the case a stat cannot see: stat follows the link, finds
// nothing at the far end, and reports the name free. The write then follows the
// link too and creates the file at the TARGET — outside the working directory,
// somewhere the user never offered up. O_EXCL refuses a name that exists at
// all, a dangling link included, and takes the next one.
//
// The link must dangle. Pointed at a file that already exists, stat succeeds,
// the old code moves to chart-2.png of its own accord, and the test passes
// against the very implementation it exists to reject.
func TestSaveSharedFileDoesNotWriteThroughASymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation needs privilege on Windows")
	}
	cwd := testsupport.TempDir(t)
	outside := filepath.Join(testsupport.TempDir(t), "planted.txt")
	// The name the save is about to choose is a link to a file that does not
	// exist yet, so writing through it CREATES that file.
	if err := os.Symlink(outside, filepath.Join(cwd, "chart.png")); err != nil {
		t.Fatal(err)
	}

	c := newShareCarrier(map[string][]byte{"shr_a": []byte("BODY")})
	i := sharedActionInteractive(t, c, cwd, true, []ctrlproto.SharedFileEntry{
		entry("shr_a", "chart.png", "/daemon/side/shr_a-chart.png"),
	})

	i.saveSharedFile("shr_a")

	if _, err := os.Lstat(outside); err == nil {
		t.Fatalf("the save followed the symlink and wrote outside the working directory, to %s", outside)
	}
	if got, err := os.ReadFile(filepath.Join(cwd, "chart-2.png")); err != nil || string(got) != "BODY" {
		t.Errorf("the copy did not land beside the link: %q, %v", got, err)
	}
}

// The saved copy is owner-only.
//
// These bytes came off the agent and may be an export, a dump, or a report
// built from something private. The share store wrote them 0o600 on the
// daemon's disk; a copy that lands 0o644 in the working directory quietly
// widens that to every local account on a multi-user host. The umask cannot be
// relied on to do it — plenty of developer machines run 022.
func TestASavedShareIsOwnerOnly(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix permission bits do not carry over")
	}
	cwd := testsupport.TempDir(t)
	c := newShareCarrier(map[string][]byte{"shr_a": []byte("BODY")})
	// The saved name comes off the FETCH, not the listing row.
	c.name = "secrets.csv"
	i := sharedActionInteractive(t, c, cwd, true, []ctrlproto.SharedFileEntry{
		entry("shr_a", "secrets.csv", "/daemon/side/shr_a-secrets.csv"),
	})

	i.saveSharedFile("shr_a")

	info, err := os.Stat(filepath.Join(cwd, "secrets.csv"))
	if err != nil {
		t.Fatalf("the file was not saved: %v", err)
	}
	if got := info.Mode().Perm(); got != privfs.FileMode {
		t.Errorf("saved mode = %#o, want %#o — the copy is readable beyond its owner", got, privfs.FileMode)
	}
}

// Saving the same share twice keeps both copies. The second save must not
// quietly replace the first: each one is a file the user asked for.
func TestSavingTheSameShareTwiceKeepsBoth(t *testing.T) {
	cwd := testsupport.TempDir(t)
	c := newShareCarrier(map[string][]byte{"shr_a": []byte("BODY")})
	i := sharedActionInteractive(t, c, cwd, true, []ctrlproto.SharedFileEntry{
		entry("shr_a", "chart.png", "/daemon/side/shr_a-chart.png"),
	})

	i.saveSharedFile("shr_a")
	i.saveSharedFile("shr_a")

	for _, name := range []string{"chart.png", "chart-2.png"} {
		if got, err := os.ReadFile(filepath.Join(cwd, name)); err != nil || string(got) != "BODY" {
			t.Errorf("%s = %q, %v — want both saves kept", name, got, err)
		}
	}
}

// The name came off the daemon's store and crosses a machine boundary to be
// written into the USER's tree, so it is re-checked here rather than trusted.
func TestSaveSharedFileRefusesToEscapeTheWorkingDirectory(t *testing.T) {
	cwd := testsupport.TempDir(t)
	c := newShareCarrier(map[string][]byte{"shr_a": []byte("BODY")})
	c.name = "../escaped.txt"
	i := sharedActionInteractive(t, c, cwd, true, []ctrlproto.SharedFileEntry{
		entry("shr_a", "escaped.txt", "/daemon/side/shr_a-escaped.txt"),
	})

	i.saveSharedFile("shr_a")

	if _, err := os.Stat(filepath.Join(filepath.Dir(cwd), "escaped.txt")); err == nil {
		t.Fatal("a traversing name wrote outside the working directory")
	}
	// It still lands, under the flattened name: refusing the save entirely
	// would be a worse answer than saving it somewhere safe.
	if entries, _ := os.ReadDir(cwd); len(entries) != 1 {
		t.Errorf("cwd holds %d entries, want the one saved copy", len(entries))
	}
}

func TestSanitizeSavedName(t *testing.T) {
	for in, want := range map[string]string{
		"report.pdf":     "report.pdf",
		"../../etc/pass": "pass",
		"/abs/path.txt":  "path.txt",
		".hidden":        "hidden",
		"":               "shared-file",
		"..":             "shared-file",
		".":              "shared-file",
		// An escape sequence goes whole. Windows rejects the control byte in a
		// filename outright, so a name carrying one could not be saved there at
		// all — and everywhere else it paints itself into any listing of the
		// directory. Dropping only the ESC would leave "[31m" printing as text,
		// which is why this delegates to the sequence-aware pass.
		"report\x1b[31m\x1b[2J\x1b]0;pwned\x07.pdf": "report.pdf",
		// A name that is nothing BUT a sequence reduces to nothing, so it takes
		// the fallback rather than an empty filename.
		"\x1b[2J": "shared-file",
	} {
		if got := sanitizeSavedName(in); got != want {
			t.Errorf("sanitizeSavedName(%q) = %q, want %q", in, got, want)
		}
	}
}

// A path on the daemon's host is not this host's to copy: pasted into a
// terminal here it opens nothing, or something else with the same name.
func TestCopyAndOpenRefuseOnARemoteCarrier(t *testing.T) {
	cwd := testsupport.TempDir(t)
	c := newShareCarrier(nil)
	i := sharedActionInteractive(t, c, cwd, false, []ctrlproto.SharedFileEntry{
		entry("shr_a", "report.pdf", "/daemon/side/shr_a-report.pdf"),
	})

	i.copySharedPath("shr_a")
	notice := dialogNotice(i)
	if !strings.Contains(notice, "daemon's host") {
		t.Errorf("copy on a remote carrier should say why it refused:\n%s", notice)
	}
	if !strings.Contains(notice, "save a copy") {
		t.Errorf("the refusal should point at the verb that does work:\n%s", notice)
	}

	i.openSharedFile("shr_a")
	if notice := dialogNotice(i); !strings.Contains(notice, "daemon's host") {
		t.Errorf("open on a remote carrier should refuse too:\n%s", notice)
	}
}

// On a LOCAL carrier, copy actually copies.
//
// This is the half the suite never asserted, and its absence was not academic:
// replacing the gate with `if true` — copy and open refuse on every carrier,
// including the common one — left the whole package green. A suite that only
// pins the refusals cannot tell "correctly cautious" from "broken for
// everyone", because both look like a refusal.
//
// The clipboard is behind a var for exactly this: the real one shells out to
// xclip or wl-copy, which a test must not run.
func TestCopyOnALocalCarrierPutsThePathOnTheClipboard(t *testing.T) {
	var got string
	calls := 0
	defer swapClipboard(func(s string) error {
		got, calls = s, calls+1
		return nil
	})()

	cwd := testsupport.TempDir(t)
	c := newShareCarrier(nil)
	i := sharedActionInteractive(t, c, cwd, true, []ctrlproto.SharedFileEntry{
		entry("shr_a", "report.pdf", "/daemon/side/shr_a-report.pdf"),
	})

	i.copySharedPath("shr_a")

	if calls != 1 {
		t.Fatalf("the clipboard was written %d times, want exactly one", calls)
	}
	if got != "/daemon/side/shr_a-report.pdf" {
		t.Errorf("copied %q, want the listing's path", got)
	}
	if notice := dialogNotice(i); !strings.Contains(notice, "copied") {
		t.Errorf("the panel does not confirm the copy:\n%s", notice)
	}
}

// And open actually opens, for the same reason.
func TestOpenOnALocalCarrierHandsThePathToTheViewer(t *testing.T) {
	var got string
	calls := 0
	defer swapViewer(func(s string) error {
		got, calls = s, calls+1
		return nil
	})()

	cwd := testsupport.TempDir(t)
	c := newShareCarrier(nil)
	i := sharedActionInteractive(t, c, cwd, true, []ctrlproto.SharedFileEntry{
		entry("shr_a", "chart.png", "/daemon/side/shr_a-chart.png"),
	})

	i.openSharedFile("shr_a")

	if calls != 1 {
		t.Fatalf("the viewer was launched %d times, want exactly one", calls)
	}
	if got != "/daemon/side/shr_a-chart.png" {
		t.Errorf("opened %q, want the listing's path", got)
	}
	if notice := dialogNotice(i); !strings.Contains(notice, "opened chart.png") {
		t.Errorf("the panel does not confirm the open:\n%s", notice)
	}
}

// A clipboard that refuses is reported rather than swallowed: the user must not
// be told "copied" about a clipboard that received nothing. Windows reaches
// this path for real — it compiles into clipboard_other.go, which always
// refuses — so it is the answer a whole platform gets today.
func TestCopyReportsAClipboardFailure(t *testing.T) {
	defer swapClipboard(func(string) error {
		return errors.New("no clipboard backend detected")
	})()

	cwd := testsupport.TempDir(t)
	c := newShareCarrier(nil)
	i := sharedActionInteractive(t, c, cwd, true, []ctrlproto.SharedFileEntry{
		entry("shr_a", "report.pdf", "/daemon/side/shr_a-report.pdf"),
	})

	i.copySharedPath("shr_a")

	notice := dialogNotice(i)
	if !strings.Contains(notice, "copy failed") || !strings.Contains(notice, "no clipboard backend") {
		t.Errorf("a failed copy should say so, and why:\n%s", notice)
	}
}

// A remote carrier must not reach the clipboard at all. The refusal test next
// door reads the notice; this one watches the seam, so a gate that refuses in
// prose while still writing could not pass both.
func TestARemoteCarrierNeverTouchesTheClipboard(t *testing.T) {
	calls := 0
	defer swapClipboard(func(string) error { calls++; return nil })()
	viewerCalls := 0
	defer swapViewer(func(string) error { viewerCalls++; return nil })()

	cwd := testsupport.TempDir(t)
	c := newShareCarrier(nil)
	i := sharedActionInteractive(t, c, cwd, false, []ctrlproto.SharedFileEntry{
		entry("shr_a", "report.pdf", "/daemon/side/shr_a-report.pdf"),
	})

	i.copySharedPath("shr_a")
	i.openSharedFile("shr_a")

	if calls != 0 || viewerCalls != 0 {
		t.Errorf("a remote carrier reached out %d times (clipboard) and %d times (viewer), want none", calls, viewerCalls)
	}
}

// A listing entry with no path (a daemon that did not send one) is the same
// case as a remote one: there is nothing to copy or open.
func TestCopyRefusesWithoutAPath(t *testing.T) {
	cwd := testsupport.TempDir(t)
	c := newShareCarrier(nil)
	i := sharedActionInteractive(t, c, cwd, true, []ctrlproto.SharedFileEntry{
		entry("shr_a", "report.pdf", ""),
	})

	i.copySharedPath("shr_a")
	if notice := dialogNotice(i); !strings.Contains(notice, "save a copy") {
		t.Errorf("a pathless entry should point at save:\n%s", notice)
	}
}

// The listing is refetched, so a row can vanish between the keypress and the
// action. Saying so beats acting on a stale id.
func TestActionsOnAVanishedRowSaySo(t *testing.T) {
	cwd := testsupport.TempDir(t)
	c := newShareCarrier(nil)
	i := sharedActionInteractive(t, c, cwd, true, nil)

	i.copySharedPath("shr_gone")
	if notice := dialogNotice(i); !strings.Contains(notice, "no longer listed") {
		t.Errorf("copy on a vanished row should say so:\n%s", notice)
	}
	i.openSharedFile("shr_gone")
	if notice := dialogNotice(i); !strings.Contains(notice, "no longer listed") {
		t.Errorf("open on a vanished row should say so:\n%s", notice)
	}
}

// A failed fetch is reported, not silently swallowed into an empty file.
func TestSaveReportsAFailedFetch(t *testing.T) {
	cwd := testsupport.TempDir(t)
	c := newShareCarrier(nil)
	c.err = ctrlproto.Errorf(ctrlproto.CodeNotFound, "swept")
	i := sharedActionInteractive(t, c, cwd, true, []ctrlproto.SharedFileEntry{
		entry("shr_a", "gone.pdf", "/daemon/side/shr_a-gone.pdf"),
	})

	i.saveSharedFile("shr_a")

	if notice := dialogNotice(i); !strings.Contains(notice, "save failed") {
		t.Errorf("a failed fetch should be reported:\n%s", notice)
	}
	if entries, _ := os.ReadDir(cwd); len(entries) != 0 {
		t.Errorf("a failed save left %d files behind", len(entries))
	}
}

// The listing verb fills the cache the panel renders from.
func TestRefreshSharedFilesFillsTheCache(t *testing.T) {
	c := newShareCarrier(nil)
	c.list = []ctrlproto.SharedFileEntry{entry("shr_a", "report.pdf", "/daemon/shr_a-report.pdf")}
	i := newCtrlprotoTestInteractive()
	i.cfg.Carrier = c
	i.cfg.CarrierSession = "s1"

	if err := i.refreshSharedFiles(); err != nil {
		t.Fatalf("refreshSharedFiles: %v", err)
	}
	rows := i.sharedFileRows()
	if len(rows) != 1 || rows[0].ID != "shr_a" {
		t.Fatalf("cache = %+v, want the listed file", rows)
	}
}

// A carrier that does not serve the verbs must produce a clean "not available"
// rather than an error about a method nobody can call.
func TestRefreshSharedFilesOnACarrierWithoutTheVerbs(t *testing.T) {
	i := newCtrlprotoTestInteractive()
	i.cfg.Carrier = newFakeCarrier() // no SharedFilesController
	i.cfg.CarrierSession = "s1"

	if err := i.refreshSharedFiles(); err == nil {
		t.Fatal("a carrier without the verbs should refuse")
	}
}
