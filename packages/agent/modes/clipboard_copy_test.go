package modes

import (
	"errors"
	"strings"
	"testing"

	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/tui"
	"terva.sh/terva/packages/tui/tuitest"
)

// swapClipboardRoute pins which mechanism copyText reaches for, so a test
// asserts the route it means rather than whatever ssh variables the
// developer's shell happens to carry.
func swapClipboardRoute(r tui.ClipboardRoute) func() {
	prev := clipboardRouteFn
	clipboardRouteFn = func() tui.ClipboardRoute { return r }
	return func() { clipboardRouteFn = prev }
}

func withReply(t *testing.T, text string) *Interactive {
	t.Helper()
	i := &Interactive{}
	i.setCarrierTranscript([]core.WireMessage{
		core.MessageToWire(provider.Message{
			Role:    provider.RoleUser,
			Content: []provider.Content{provider.TextBlock{Text: "write me something"}},
		}),
		core.MessageToWire(provider.Message{
			Role:    provider.RoleAssistant,
			Content: []provider.Content{provider.TextBlock{Text: text}},
		}),
	})
	return i
}

func statusOf(t *testing.T, i *Interactive) (ok, err string) {
	t.Helper()
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.statusOK, i.statusErr
}

const pyReply = "Here is the fix:\n\n```python\ndef go():\n    if x:\n        return 1\n    return 0\n```\n\nThat should do it."

// The reason /copy exists. Dragging the reply out of the terminal gets
// you what the screen holds: every row carries the two-column prose
// gutter, and any row longer than the pane carries a newline terva put
// there. /copy hands back the source instead, which for Python is the
// difference between code that runs and code that raises
// IndentationError on its first line.
func TestCopyLastReplyGivesTheSourceNotTheScreen(t *testing.T) {
	defer swapClipboardRoute(tui.ClipboardLocal)()
	got := ""
	defer swapClipboard(func(s string) error { got = s; return nil })()

	i := withReply(t, pyReply)
	i.copyLastReply("")

	if got != pyReply {
		t.Fatalf("copied:\n%q\nwant:\n%q", got, pyReply)
	}

	// And show what the screen would have handed over instead, so this
	// stays a test about the difference rather than about a string.
	v := &tui.View{Theme: tui.Dark}
	v.Messages = []provider.Message{{
		Role:    provider.RoleAssistant,
		Content: []provider.Content{provider.TextBlock{Text: pyReply}},
	}}
	var indented int
	for _, row := range v.Build(60) {
		if strings.HasPrefix(row, "  ") && strings.TrimSpace(row) != "" {
			indented++
		}
	}
	if indented == 0 {
		t.Fatal("the rendered reply carries no left gutter; this test no longer describes the bug it guards")
	}
	if strings.HasPrefix(got, " ") {
		t.Errorf("copied text starts with the render gutter: %q", got)
	}
}

func TestCopyLastReplyCodeExtractsTheBlock(t *testing.T) {
	defer swapClipboardRoute(tui.ClipboardLocal)()
	got := ""
	defer swapClipboard(func(s string) error { got = s; return nil })()

	i := withReply(t, pyReply)
	i.copyLastReply("code")

	want := "def go():\n    if x:\n        return 1\n    return 0"
	if got != want {
		t.Fatalf("copied:\n%q\nwant:\n%q", got, want)
	}
	if ok, _ := statusOf(t, i); !strings.Contains(ok, "code block") {
		t.Errorf("status = %q, want it to name the code block", ok)
	}
}

func TestCopyLastReplyRejectsUnknownArgAndCopiesNothing(t *testing.T) {
	defer swapClipboardRoute(tui.ClipboardLocal)()
	called := false
	defer swapClipboard(func(string) error { called = true; return nil })()

	i := withReply(t, pyReply)
	i.copyLastReply("everything")

	if called {
		t.Error("copied despite a bad argument")
	}
	if _, errMsg := statusOf(t, i); errMsg == "" {
		t.Error("no error status for a bad argument")
	}
}

func TestCopyLastReplyWithNoReply(t *testing.T) {
	defer swapClipboardRoute(tui.ClipboardLocal)()
	called := false
	defer swapClipboard(func(string) error { called = true; return nil })()

	i := &Interactive{}
	i.copyLastReply("")

	if called {
		t.Error("copied from an empty transcript")
	}
	if _, errMsg := statusOf(t, i); errMsg == "" {
		t.Error("no error status for an empty transcript")
	}
}

func TestCopyLastReplyNoCodeBlock(t *testing.T) {
	defer swapClipboardRoute(tui.ClipboardLocal)()
	called := false
	defer swapClipboard(func(string) error { called = true; return nil })()

	i := withReply(t, "just prose, no fences here")
	i.copyLastReply("code")

	if called {
		t.Error("copied when there was no code block")
	}
}

// On a remote session the local tools address the wrong machine: they
// would put the text on the clipboard of a host the user never pastes
// from. The terminal route has to win outright, not merely be tried.
func TestRemoteSessionCopiesThroughTheTerminal(t *testing.T) {
	defer swapClipboardRoute(tui.ClipboardTerminal)()
	localCalled := false
	defer swapClipboard(func(string) error { localCalled = true; return nil })()

	term := tuitest.NewFakeTerm(80, 24)
	i := &Interactive{cfg: InteractiveConfig{Terminal: term}}

	viaTerminal, err := i.copyText("hello")
	if err != nil {
		t.Fatalf("copyText: %v", err)
	}
	if !viaTerminal {
		t.Error("viaTerminal = false; the caller would then claim a verified copy")
	}
	if localCalled {
		t.Error("ran the local clipboard tool on a remote session")
	}
	want, _ := tui.OSC52Copy("hello")
	if !strings.Contains(term.Output(), want) {
		t.Errorf("OSC 52 sequence not written to the terminal")
	}
}

// A platform with no clipboard tool (clipboard_other.go returns an error
// on every call) still has a terminal to ask.
func TestLocalToolFailureFallsBackToTheTerminal(t *testing.T) {
	defer swapClipboardRoute(tui.ClipboardLocal)()
	defer swapClipboard(func(string) error { return errors.New("no clipboard backend detected") })()

	term := tuitest.NewFakeTerm(80, 24)
	i := &Interactive{cfg: InteractiveConfig{Terminal: term}}

	viaTerminal, err := i.copyText("hello")
	if err != nil {
		t.Fatalf("copyText: %v", err)
	}
	if !viaTerminal {
		t.Error("viaTerminal = false, want the OSC 52 fallback to be reported")
	}
	want, _ := tui.OSC52Copy("hello")
	if !strings.Contains(term.Output(), want) {
		t.Error("OSC 52 fallback not written to the terminal")
	}
}

// Both routes gone: report the LOCAL tool's error, which is the one the
// user can act on ("install wl-clipboard" beats "your terminal ignored a
// sequence").
func TestCopyTextReportsTheActionableError(t *testing.T) {
	defer swapClipboardRoute(tui.ClipboardLocal)()
	defer swapClipboard(func(string) error { return errors.New("xclip not found") })()

	i := &Interactive{} // no Terminal, so the OSC 52 route cannot run either
	if _, err := i.copyText("hello"); err == nil || !strings.Contains(err.Error(), "xclip") {
		t.Fatalf("err = %v, want the local tool's message", err)
	}
}

func TestLastFencedBlock(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
		ok   bool
	}{
		{"none", "just prose", "", false},
		{"single", "a\n```\nx = 1\n```\nb", "x = 1", true},
		{"last of several", "```\nfirst\n```\ntext\n```go\nsecond\n```", "second", true},
		{"keeps inner indentation", "```py\nif a:\n    b()\n```", "if a:\n    b()", true},
		{"strips the fence's own indent", "  ```\n  x = 1\n  ```", "x = 1", true},
		// A reply cut off mid-code — cancelled, or out of tokens — is
		// exactly when reaching for /copy code is most likely.
		{"unterminated", "```python\ndef f():\n    return 1", "def f():\n    return 1", true},
		{"empty fence", "```\n```", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := lastFencedBlock(tc.in)
			if ok != tc.ok || got != tc.want {
				t.Errorf("lastFencedBlock(%q) = %q,%v; want %q,%v", tc.in, got, ok, tc.want, tc.ok)
			}
		})
	}
}
