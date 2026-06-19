package modes

// The global keymap (TUI plan Phase 1.5): every chord that works
// outside dialogs and popups is one named entry in a table instead of
// a case in a hand-grown switch. The table is the contract — what
// each chord does is visible in one place, bindings dispatch in
// order, and a future user-rebinding layer has a data structure to
// hang configuration off.
//
// Dispatch semantics: bindings are consulted in table order; the
// first entry matching the key runs. An entry may report keyPass to
// decline (e.g. Alt+Up with an empty queue, Esc while a popup is
// open) — the scan then continues to later matching entries and,
// past the table, to the downstream handlers in handleKey (popup
// interception, tab-complete, input history, the editor itself).

import (
	"context"

	"terva.sh/terva/packages/tui"
)

// keyOutcome is a binding's verdict on a key press.
type keyOutcome int

const (
	// keyPass declines the key: keep scanning the table, then fall
	// through to handleKey's downstream consumers.
	keyPass keyOutcome = iota
	// keyHandled consumes the key.
	keyHandled
	// keyQuit consumes the key and exits the interactive loop.
	keyQuit
)

// globalBinding maps one key chord to a named action.
type globalBinding struct {
	kind tui.KeyKind
	alt  bool   // chord requires Alt held
	name string // stable action name (docs, future rebind config)
	run  func(ctx context.Context, k tui.Key) keyOutcome
}

// dispatchGlobalKey runs k through the keymap. Reports whether the
// key was consumed and whether the loop should exit.
func (i *Interactive) dispatchGlobalKey(ctx context.Context, k tui.Key) (handled, done bool) {
	for _, b := range i.keymap {
		if b.kind != k.Kind {
			continue
		}
		if b.alt && !k.Alt {
			continue
		}
		switch b.run(ctx, k) {
		case keyQuit:
			return true, true
		case keyHandled:
			return true, false
		case keyPass:
			// Binding declined; later table entries may still match.
		}
	}
	return false, false
}

// buildGlobalKeymap assembles the table. Order matters only where two
// entries share a key kind (Alt+Up before Up).
func (i *Interactive) buildGlobalKeymap() []globalBinding {
	return []globalBinding{
		{kind: tui.KeyCtrlC, name: "exit-or-clear", run: i.keyCtrlC},
		{kind: tui.KeyEsc, name: "dismiss-or-cancel", run: i.keyEsc},
		{kind: tui.KeyCtrlD, name: "quit-when-idle", run: i.keyCtrlD},
		{kind: tui.KeyCtrlL, name: "repaint", run: i.keyRepaint},
		{kind: tui.KeyCtrlO, name: "toggle-tool-expand", run: i.keyToggleExpand},
		{kind: tui.KeyPageUp, name: "scroll-page-up", run: func(context.Context, tui.Key) keyOutcome {
			// The slash popup pages its own catalog; only it gets the
			// key (the @-file popup has no pagination, so chat
			// scrolling stays available while it is open).
			if i.suggest.Active(i.ed.Value()) {
				return keyPass
			}
			i.scrollBy(+i.chatPage())
			return keyHandled
		}},
		{kind: tui.KeyPageDown, name: "scroll-page-down", run: func(context.Context, tui.Key) keyOutcome {
			if i.suggest.Active(i.ed.Value()) {
				return keyPass
			}
			i.scrollBy(-i.chatPage())
			return keyHandled
		}},
		{kind: tui.KeyUp, alt: true, name: "pop-queued-message", run: i.keyPopQueued},
		{kind: tui.KeyUp, name: "cursor-up-or-scroll", run: i.keyUp},
		{kind: tui.KeyDown, name: "cursor-down-or-scroll", run: i.keyDown},
		{kind: tui.KeyEnter, alt: true, name: "insert-newline", run: func(context.Context, tui.Key) keyOutcome {
			i.ed.HandleKey(tui.Key{Kind: tui.KeyRune, Rune: '\n', Alt: true})
			return keyHandled
		}},
	}
}

// keyCtrlC implements the deliberate exit protocol. While busy: do
// NOT cancel the turn — ctrl+c during a running turn is almost always
// reflex muscle memory ("be quiet" in a shell) rather than a decision
// to kill a multi-minute model call that's already cost tokens. Use
// esc to interrupt a turn; use a deliberate double-ctrl+c to exit
// terva entirely. First press arms the exit hint (or clears typed
// input), second press within ctrlCExitWindow quits.
func (i *Interactive) keyCtrlC(context.Context, tui.Key) keyOutcome {
	i.mu.Lock()
	loadingSession := i.sessionLoading
	i.mu.Unlock()
	if loadingSession {
		return keyQuit
	}
	if i.turns.Busy() {
		if i.ctrlCExitArmed() {
			return keyQuit
		}
		i.mu.Lock()
		i.statusOK = "press ctrl+c again to exit, esc to cancel the turn"
		i.statusErr = ""
		i.mu.Unlock()
		i.armCtrlCExit()
		return keyHandled
	}
	// Idle: first press clears the editor (and any queued follow-up
	// messages); a second press within ctrlCExitWindow exits. With
	// both an empty editor and no queue the first press still just
	// arms — require a deliberate double-tap.
	hadInput := !i.ed.IsEmpty() || i.turns.QueuedCount() > 0
	if hadInput {
		i.ed.Clear()
		i.suggest.Reset()
		i.turns.DrainQueued()
		i.mu.Lock()
		i.statusOK = "input cleared"
		i.statusErr = ""
		i.mu.Unlock()
		i.armCtrlCExit()
		return keyHandled
	}
	if i.ctrlCExitArmed() {
		return keyQuit
	}
	i.mu.Lock()
	i.statusOK = "press ctrl+c again to exit"
	i.statusErr = ""
	i.mu.Unlock()
	i.armCtrlCExit()
	return keyHandled
}

// keyEsc dismisses transient overlays (the /help block, extension
// notes, a parked shell-escape log) before considering the running
// turn — a casual Esc after /help on a busy turn must not rip the
// turn away. While a slash/file popup is open the key passes through
// to the popup's own Esc handling downstream.
func (i *Interactive) keyEsc(context.Context, tui.Key) keyOutcome {
	if i.suggest.Active(i.ed.Value()) || i.fileSuggest.Active(i.ed.Value()) {
		return keyPass
	}
	i.mu.Lock()
	hadHelp := len(i.helpBlock) > 0
	hadNotes := len(i.extNotes) > 0
	// Only dismiss a parked shell-escape log on esc when nothing is
	// running; if a !command is in flight, esc must fall through to
	// the cancel path below instead of just hiding the (empty) block.
	hadShell := len(i.shellBlock) > 0 && !i.shellRunning
	if hadHelp {
		i.helpBlock = nil
	}
	if hadNotes {
		i.extNotes = nil
	}
	if hadShell {
		i.shellBlock = nil
	}
	i.mu.Unlock()
	if hadHelp || hadNotes || hadShell {
		i.invalidate()
		return keyHandled
	}
	if i.turns.Busy() && i.turns.cancelActive() {
		// If a confirm or question dialog is pending, resolve it so the
		// agent goroutine unblocks and the context cancellation can
		// actually take effect.
		i.confirmDialog.CancelAll("turn cancelled")
		i.questionDialog.CancelAll()
		return keyHandled
	}
	// Idle with nothing to dismiss: the editor's own Esc handling
	// (clear the draft) takes it downstream.
	return keyPass
}

func (i *Interactive) keyCtrlD(context.Context, tui.Key) keyOutcome {
	if i.ed.IsEmpty() && !i.turns.Busy() {
		return keyQuit
	}
	return keyPass
}

func (i *Interactive) keyRepaint(context.Context, tui.Key) keyOutcome {
	i.rend.Clear()
	i.invalidate()
	return keyHandled
}

// keyToggleExpand toggles expansion of collapsed tool results.
// Affects every tool call in the transcript — press again to
// re-collapse. In main-screen scrollback mode this changes
// already-emitted transcript rows, so do a full clear+replay instead
// of trying to edit old scrollback in place.
func (i *Interactive) keyToggleExpand(context.Context, tui.Key) keyOutcome {
	i.mu.Lock()
	i.view.ExpandAll = !i.view.ExpandAll
	if i.rend != nil {
		i.rend.Clear()
	}
	i.mu.Unlock()
	i.invalidate()
	return keyHandled
}

// keyPopQueued (Alt/Option+Up) pops the most recently queued
// ("sliding in") message back into the editor so the user can edit
// and resend it. Repeated presses keep peeling messages off the tail
// of the queue; each press *replaces* the editor contents. When the
// queue is empty the key passes through to the normal Up behavior.
func (i *Interactive) keyPopQueued(context.Context, tui.Key) keyOutcome {
	text, _ := i.turns.PopQueued()
	if text == "" {
		return keyPass
	}
	i.ed.SetValue(text)
	i.inputHistoryIndex = -1
	i.invalidate()
	return keyHandled
}

// keyUp: in multi-line / wrapped input, Up first moves inside the
// editor; at the editor's top edge it falls back to chat scrolling.
// While a popup is open the key passes to the popup's navigation.
func (i *Interactive) keyUp(context.Context, tui.Key) keyOutcome {
	if i.suggest.Active(i.ed.Value()) || i.fileSuggest.Active(i.ed.Value()) {
		return keyPass
	}
	if i.ed.MoveVertical(-1) {
		i.invalidate()
		return keyHandled
	}
	i.scrollBy(+3)
	return keyHandled
}

func (i *Interactive) keyDown(context.Context, tui.Key) keyOutcome {
	if i.suggest.Active(i.ed.Value()) || i.fileSuggest.Active(i.ed.Value()) {
		return keyPass
	}
	if i.ed.MoveVertical(+1) {
		i.invalidate()
		return keyHandled
	}
	if i.scrollOffset > 0 {
		i.scrollBy(-3)
	}
	return keyHandled
}
