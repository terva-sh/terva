package modes

import (
	"context"
	"strings"
	"testing"

	"terva.sh/terva/packages/agent/modes/dialogs"
	"terva.sh/terva/packages/tui"
)

// withPicker is withReply plus the dialog the real constructor builds,
// since these tests assemble an Interactive by hand.
func withPicker(t *testing.T, reply string) *Interactive {
	t.Helper()
	i := withReply(t, reply)
	i.copyDialog = dialogs.NewCopyDialog()
	return i
}

func TestBareCopyOpensThePickerInsteadOfCopying(t *testing.T) {
	defer swapClipboardRoute(tui.ClipboardLocal)()
	called := false
	defer swapClipboard(func(string) error { called = true; return nil })()

	i := withPicker(t, pyReply)
	i.runCopyCommand("")

	if !i.copyDialog.Active() {
		t.Fatal("/copy did not open the picker")
	}
	if called {
		t.Error("/copy copied something before anything was picked")
	}
}

// The old one-shot form still works, because muscle memory was built on
// it. This is the promise made when bare /copy changed meaning.
func TestCopyLastKeepsTheOldBehaviour(t *testing.T) {
	defer swapClipboardRoute(tui.ClipboardLocal)()
	got := ""
	defer swapClipboard(func(s string) error { got = s; return nil })()

	i := withPicker(t, pyReply)
	i.runCopyCommand("last")

	if got != pyReply {
		t.Errorf("copied %q, want the whole reply", got)
	}
	if i.copyDialog.Active() {
		t.Error("/copy last opened the picker instead of copying outright")
	}
}

func TestCopyCodeKeepsTheOldBehaviour(t *testing.T) {
	defer swapClipboardRoute(tui.ClipboardLocal)()
	got := ""
	defer swapClipboard(func(s string) error { got = s; return nil })()

	i := withPicker(t, pyReply)
	i.runCopyCommand("code")

	if !strings.Contains(got, "def go():") {
		t.Errorf("copied %q, want the last code block", got)
	}
	// The one-shot form strips the fence markers; the picker keeps them.
	// Both are deliberate, so pin the difference.
	if strings.Contains(got, "```") {
		t.Errorf("/copy code should hand back bare code, got %q", got)
	}
	if i.copyDialog.Active() {
		t.Error("/copy code opened the picker")
	}
}

// Anything else is a filter, the way /jump takes one, rather than an
// error about argument shape.
func TestCopyWithAnArgumentOpensThePickerFiltered(t *testing.T) {
	i := withPicker(t, pyReply)
	i.runCopyCommand("write me")

	if !i.copyDialog.Active() {
		t.Fatal("/copy with an argument did not open the picker")
	}
	out := strings.Join(i.copyDialog.Render(tui.Theme{}, 80), "\n")
	if !strings.Contains(out, "filter:") {
		t.Errorf("the picker did not open filtered:\n%s", out)
	}
}

func TestCtrlYIsBoundToTheCopyPicker(t *testing.T) {
	i := withPicker(t, pyReply)

	var bound bool
	for _, b := range i.buildGlobalKeymap() {
		if b.kind == tui.KeyCtrlY {
			bound = true
			if b.name != "copy-picker" {
				t.Errorf("ctrl+y is bound to %q", b.name)
			}
		}
	}
	if !bound {
		t.Fatal("ctrl+y is not in the global keymap")
	}

	if got := i.keyOpenCopyPicker(context.Background(), tui.Key{Kind: tui.KeyCtrlY}); got != keyHandled {
		t.Errorf("keyOpenCopyPicker returned %v, want keyHandled", got)
	}
	if !i.copyDialog.Active() {
		t.Error("ctrl+y did not open the picker")
	}
}

// The whole wiring, end to end, through the real overlay dispatcher: a
// pick has to reach the clipboard without the test re-implementing the
// dispatch it is supposed to be checking.
func TestPickedPartReachesTheClipboardThroughTheOverlay(t *testing.T) {
	defer swapClipboardRoute(tui.ClipboardLocal)()
	got := ""
	defer swapClipboard(func(s string) error { got = s; return nil })()

	i := withPicker(t, pyReply)
	i.overlays = i.buildOverlays()
	i.runCopyCommand("")

	send := func(k tui.Key) {
		t.Helper()
		if handled, _ := i.dispatchOverlayKey(k); !handled {
			t.Fatal("the overlay registry did not claim the key; the copy entry is not wired")
		}
	}

	send(tui.Key{Kind: tui.KeyEnter}) // descend into turn 1
	for _, r := range "fence" {       // the kind name narrows to the code
		send(tui.Key{Kind: tui.KeyRune, Rune: r})
	}
	send(tui.Key{Kind: tui.KeyEnter}) // copy

	if !strings.Contains(got, "def go():") {
		t.Fatalf("clipboard = %q, want the python fence", got)
	}
	// Source, markers included: this is what makes it paste as code.
	if !strings.HasPrefix(got, "```python") || !strings.HasSuffix(got, "```") {
		t.Errorf("the fence lost its markers: %q", got)
	}
	if i.copyDialog.Active() {
		t.Error("the picker stayed open after copying")
	}
	if ok, _ := statusOf(t, i); !strings.Contains(ok, "turn 1") {
		t.Errorf("notice = %q, want it to name the turn", ok)
	}
}

// ctrl+y from the turn list copies the whole reply without descending.
func TestCtrlYInsideThePickerCopiesTheWholeReply(t *testing.T) {
	defer swapClipboardRoute(tui.ClipboardLocal)()
	got := ""
	defer swapClipboard(func(s string) error { got = s; return nil })()

	i := withPicker(t, pyReply)
	i.overlays = i.buildOverlays()
	i.runCopyCommand("")

	if handled, _ := i.dispatchOverlayKey(tui.Key{Kind: tui.KeyCtrlY}); !handled {
		t.Fatal("the overlay did not claim ctrl+y")
	}
	if got != pyReply {
		t.Errorf("clipboard = %q, want the whole reply", got)
	}
}

func TestCopyPickerRefusesAnEmptyTranscript(t *testing.T) {
	defer swapClipboardRoute(tui.ClipboardLocal)()
	called := false
	defer swapClipboard(func(string) error { called = true; return nil })()

	i := &Interactive{copyDialog: dialogs.NewCopyDialog()}
	i.runCopyCommand("")

	if i.copyDialog.Active() {
		t.Error("the picker opened on an empty session")
	}
	if called {
		t.Error("something was copied from an empty session")
	}
	if _, errMsg := statusOf(t, i); errMsg == "" {
		t.Error("no error status for an empty session")
	}
}
