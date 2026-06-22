package modes

// Scripted-key harness for Interactive (TUI plan Phase 0.2): runs the
// real Run() loop against a fake terminal, types bytes the way a user
// would, and asserts on the VT-emulated screen — the first tests that
// exercise interactive.go's event loop end-to-end.
//
// The harness is deliberately agent-less: cfg.Agent is nil, which is
// the real "not logged in" startup state. The login dialog that
// auto-opens is part of that contract and the tests script their way
// out of it with Esc.

import (
	"context"
	"strings"
	"testing"
	"time"

	"terva.sh/terva/packages/tui"
	"terva.sh/terva/packages/tui/tuitest"
)

type harness struct {
	t    *testing.T
	term *tuitest.FakeTerm
	i    *Interactive
	done chan error
	stop context.CancelFunc
}

// startInteractive boots a real Interactive on a fake 80x24 terminal.
// mutate, if non-nil, adjusts the config before construction.
func startInteractive(t *testing.T, mutate func(*InteractiveConfig)) *harness {
	t.Helper()
	// Pin host-environment detection so tests behave the same in any
	// terminal (or none).
	t.Setenv("TERM_PROGRAM", "")
	noImages := false
	term := tuitest.NewFakeTerm(80, 24)
	cfg := InteractiveConfig{
		Terminal:            term,
		Theme:               tui.Dark,
		Model:               "test-model",
		Provider:            "test",
		CWD:                 t.TempDir(),
		TervaHome:           t.TempDir(),
		Version:             "v0.0.0-test",
		PersonaName:         "Mieli",
		PersonaPhonetic:     "MYEH-lee",
		InlineImagesEnabled: &noImages,
	}
	if mutate != nil {
		mutate(&cfg)
	}
	i := NewInteractive(cfg)
	ctx, cancel := context.WithCancel(context.Background())
	h := &harness{t: t, term: term, i: i, done: make(chan error, 1), stop: cancel}
	go func() { h.done <- i.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		term.CloseInput()
		select {
		case <-h.done:
		case <-time.After(5 * time.Second):
			t.Error("Interactive.Run did not exit after context cancel")
		}
	})
	return h
}

// waitScreen polls the emulated screen until pred is satisfied,
// failing the test with a full screen dump on timeout. The TUI paints
// asynchronously (throttled redraws, 120ms animation ticks), so
// screen assertions must wait rather than read immediately.
func (h *harness) waitScreen(desc string, pred func(*tuitest.Screen) bool) {
	h.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if pred(h.term.Screen()) {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	h.t.Fatalf("timed out waiting for %s; screen:\n%s", desc, h.term.Screen().Text())
}

func (h *harness) waitText(snippet string) {
	h.t.Helper()
	h.waitScreen("screen to contain "+snippet, func(s *tuitest.Screen) bool {
		return strings.Contains(s.Text(), snippet)
	})
}

func (h *harness) waitGone(snippet string) {
	h.t.Helper()
	h.waitScreen("screen to no longer contain "+snippet, func(s *tuitest.Screen) bool {
		return !strings.Contains(s.Text(), snippet)
	})
}

// dismissLoginDialog scripts past the auto-opened login dialog that
// every agent-less startup shows.
func (h *harness) dismissLoginDialog() {
	h.t.Helper()
	h.waitText("── login")
	h.term.Type("\x1b") // Esc
	h.waitGone("── login")
}

func TestInteractiveStartupShowsWelcomeAndLogin(t *testing.T) {
	h := startInteractive(t, nil)
	h.waitText("i'm Mieli")
	h.waitText("── login")
	// Not logged in: the status line must say so.
	h.waitText("not logged in")
}

func TestInteractiveTypingEchoesInEditor(t *testing.T) {
	h := startInteractive(t, nil)
	h.dismissLoginDialog()
	h.term.Type("hello tui harness")
	h.waitText("hello tui harness")

	// Esc clears the draft.
	h.term.Type("\x1b")
	h.waitGone("hello tui harness")
}

func TestInteractiveSlashSuggestPopup(t *testing.T) {
	h := startInteractive(t, nil)
	h.dismissLoginDialog()
	h.term.Type("/mod")
	// The suggest popup filters the catalog down to /model.
	h.waitText("/model")
	// Tab completes the highlighted command into the editor.
	h.term.Type("\t")
	h.waitText("/model")
	// Esc-clearing the editor must also drop the popup's source text.
	h.term.Type("\x1b")
	h.waitGone("/model")
}

// TestInteractiveSlashPopupPagination: PageDown must page the open
// slash popup instead of scrolling the chat (the global scroll
// binding used to shadow the popup's pagination entirely).
func TestInteractiveSlashPopupPagination(t *testing.T) {
	h := startInteractive(t, nil)
	h.dismissLoginDialog()
	h.term.Type("/")
	h.waitText("/login")   // page 1
	h.term.Type("\x1b[6~") // PageDown
	h.waitText("/compact") // page 2 content
	h.waitGone("/login")
	h.term.Type("\x1b[5~") // PageUp back
	h.waitText("/login")
	h.term.Type("\x1b") // Esc closes the popup and clears the editor
	h.waitGone("/login")
}

func TestInteractiveHelpBlock(t *testing.T) {
	h := startInteractive(t, nil)
	h.dismissLoginDialog()
	h.term.Type("/help\r")
	// The help block is taller than the 24-row screen; its header
	// scrolls off the top, so assert on rows near its bottom.
	h.waitText("pgup / pgdn")
	h.waitText("expand / collapse long tool results")
	// Esc dismisses the help block.
	h.term.Type("\x1b")
	h.waitGone("pgup / pgdn")
}

// TestInteractiveDialogRegistryRoundTrip drives two dialogs through
// the overlay registry end-to-end: slash command opens, registry
// renders, registry-level Ctrl+C closes.
func TestInteractiveDialogRegistryRoundTrip(t *testing.T) {
	h := startInteractive(t, nil)
	h.dismissLoginDialog()

	h.term.Type("/model\r")
	h.waitText("── model")
	h.term.Type("\x03") // Ctrl+C closes via the entry's ctrlC hook
	h.waitGone("── model")

	// The settings body is taller than the 24-row screen; the scroll
	// window must keep the header and the first (cursor) item visible
	// and advertise the clipped remainder.
	h.term.Type("/settings\r")
	h.waitText("── settings")
	h.waitText("render images when supported")
	h.waitText("more below")
	h.term.Type("\x03")
	h.waitGone("── settings")
}

// TestInteractiveSlashAliasDispatch submits "/tg" — an alias, and not
// a prefix of any catalog name, so the popup can't intercept it and
// the editor submit path must resolve it. Before the slash registry,
// aliases were rejected there as unknown commands.
func TestInteractiveSlashAliasDispatch(t *testing.T) {
	h := startInteractive(t, nil)
	h.dismissLoginDialog()
	h.term.Type("/tg\r")
	// /connect runs and reports connector state (exact copy depends
	// on compiled-in connectors); the old bug surfaced "unknown
	// command" instead.
	h.waitScreen("connector status (not an unknown-command error)", func(s *tuitest.Screen) bool {
		txt := s.Text()
		if strings.Contains(txt, "unknown command") {
			return false
		}
		return strings.Contains(txt, "not configured") ||
			strings.Contains(txt, "no chat connectors")
	})
}

// TestInteractiveCtrlLRepaints drives the "repaint" keymap binding:
// Ctrl+L must clear and replay the full frame with the draft intact.
func TestInteractiveCtrlLRepaints(t *testing.T) {
	h := startInteractive(t, nil)
	h.dismissLoginDialog()
	h.term.Type("draft survives repaint")
	h.waitText("draft survives repaint")
	h.term.Type("\x0c") // Ctrl+L
	h.waitText("draft survives repaint")
	h.waitText("i'm Mieli")
}

func TestInteractiveCtrlDExitsCleanly(t *testing.T) {
	h := startInteractive(t, nil)
	h.dismissLoginDialog()
	h.waitText("i'm Mieli")
	// The status bar (provider/model line of the bottom band) is on
	// screen while running.
	h.waitText("(test) test-model")
	h.term.Type("\x04") // Ctrl+D on an empty editor quits
	var runErr error
	select {
	case runErr = <-h.done:
		// Re-feed immediately so the cleanup's wait finds it even if
		// an assertion below fails.
		h.done <- runErr
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not exit after Ctrl+D")
	}
	if runErr != nil {
		t.Fatalf("Run returned %v, want nil", runErr)
	}
	// Exit must tear down the live band (TeardownLog): the transcript
	// (welcome banner, status notes) stays, the input/status chrome
	// does not.
	txt := h.term.Screen().Text()
	if !strings.Contains(txt, "i'm Mieli") {
		t.Fatalf("transcript should survive teardown; screen:\n%s", txt)
	}
	if strings.Contains(txt, "(test) test-model") {
		t.Fatalf("status band survived teardown; screen:\n%s", txt)
	}
}

func TestInteractiveResizeRepaints(t *testing.T) {
	h := startInteractive(t, nil)
	h.dismissLoginDialog()
	h.term.Type("resize draft")
	h.waitText("resize draft")
	h.term.Resize(60, 16)
	// After SIGWINCH the frame repaints at the new size with the
	// draft intact.
	h.waitText("resize draft")
	h.waitText("i'm Mieli")
}

// TestInteractiveResizeWhileTypingStress interleaves resize callbacks
// (which run on the signal-handler goroutine in production) with
// typing on the main loop. Under -race this pins the invariant that
// every Renderer access is serialized on i.mu — the harness caught a
// real Resize-vs-DrawLog data race here when it was first written.
func TestInteractiveResizeWhileTypingStress(t *testing.T) {
	h := startInteractive(t, nil)
	h.dismissLoginDialog()
	sizes := [][2]int{{60, 16}, {100, 30}, {80, 24}, {72, 20}}
	for round := range 12 {
		h.term.Type("xyzzy")
		s := sizes[round%len(sizes)]
		h.term.Resize(s[0], s[1])
		h.term.Type("\x1b") // clear the draft again
	}
	h.waitText("i'm Mieli")
}
