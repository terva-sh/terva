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

	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/i18n"
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
	kind  tui.KeyKind
	alt   bool   // chord requires Alt held
	shift bool   // chord requires Shift held
	ctrl  bool   // chord requires Ctrl held
	name  string // stable action name (docs, future rebind config)
	run   func(ctx context.Context, k tui.Key) keyOutcome
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
		if b.shift && !k.Shift {
			continue
		}
		if b.ctrl && !k.Ctrl {
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

// keyInsertNewline inserts a newline for a modified Enter (Alt, Shift, or
// Ctrl). The real key is forwarded to the editor, whose KeyEnter handler
// turns a modified Enter into a newline while a bare Enter still submits.
// The binding exists so the chord beats the slash/@ popups downstream,
// which otherwise claim every KeyEnter to commit their selection.
func (i *Interactive) keyInsertNewline(_ context.Context, k tui.Key) keyOutcome {
	i.ed.HandleKey(k)
	return keyHandled
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
		{kind: tui.KeyCtrlS, name: "stash-draft", run: i.keyStashDraft},
		{kind: tui.KeyCtrlT, name: "cycle-tool-display", run: i.keyCycleToolDisplay},
		{kind: tui.KeyCtrlV, name: "paste-clipboard-image", run: i.keyPasteClipboard},
		{kind: tui.KeyShiftTab, name: "cycle-approval-mode", run: i.keyCycleApprovalMode},
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
		{kind: tui.KeyEnter, alt: true, name: "insert-newline", run: i.keyInsertNewline},
		{kind: tui.KeyEnter, shift: true, name: "insert-newline-shift", run: i.keyInsertNewline},
		{kind: tui.KeyEnter, ctrl: true, name: "insert-newline-ctrl", run: i.keyInsertNewline},
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
		i.statusOK = i18n.T("press ctrl+c again to exit, esc to cancel the turn")
		i.statusErr = ""
		i.mu.Unlock()
		i.armCtrlCExit()
		return keyHandled
	}
	// Idle: first press clears the editor (and any queued follow-up
	// messages); a second press within ctrlCExitWindow exits. With
	// both an empty editor and no queue the first press still just
	// arms — require a deliberate double-tap.
	hadInput := !i.ed.IsEmpty() || i.carrierQueuedCount() > 0
	if hadInput {
		i.ed.Clear()
		i.suggest.Reset()
		i.clearCarrierQueue()
		i.mu.Lock()
		i.statusOK = i18n.T("input cleared")
		i.statusErr = ""
		i.mu.Unlock()
		i.armCtrlCExit()
		return keyHandled
	}
	if i.ctrlCExitArmed() {
		return keyQuit
	}
	i.mu.Lock()
	i.statusOK = i18n.T("press ctrl+c again to exit")
	i.statusErr = ""
	i.mu.Unlock()
	i.armCtrlCExit()
	return keyHandled
}

// keyEsc dismisses transient overlays (the /help block, extension
// notes, a parked shell-escape log) before considering the running
// turn — a casual Esc after /help on a busy turn must not rip the
// turn away. The ✖/✓ status lines clear on every Esc that reaches
// here, without consuming the press. While a slash/file popup is
// open the key passes through to the popup's own Esc handling
// downstream.
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
	// The ✖/✓ status lines are chrome too — a worktree sweep's removed/kept
	// report otherwise sits pinned until the next prompt — but they RIDE
	// ALONG rather than consuming the press: a persistent ambient status
	// ("not logged in", re-asserted by anything that needs a credential)
	// would otherwise tax every draft-clearing Esc with a wasted keypress.
	hadStatus := i.statusErr != "" || i.statusOK != ""
	if hadHelp {
		i.helpBlock = nil
	}
	if hadNotes {
		i.resetNotes()
	}
	if hadShell {
		i.shellBlock = nil
	}
	if hadStatus {
		i.statusErr = ""
		i.statusOK = ""
	}
	i.mu.Unlock()
	if hadHelp || hadNotes || hadShell {
		i.invalidate()
		return keyHandled
	}
	if hadStatus {
		// Redraw without the lines; the key still falls through to the
		// cancel path / the editor, so one Esc does everything it used to.
		i.invalidate()
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

// keyPasteClipboard (ctrl+v) pastes an image from the system clipboard into
// the prompt as a "[clipboard image #N]" marker. Text paste is unaffected:
// it arrives as a bracketed-paste event, not this key. See pasteClipboard.
func (i *Interactive) keyPasteClipboard(context.Context, tui.Key) keyOutcome {
	i.pasteClipboard()
	return keyHandled
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

// keyCycleToolDisplay (ctrl+t) cycles how tool calls render: full
// bordered boxes → one-line minimal summaries → grouped (one line per
// run of calls) → hidden → back to boxes. Like ctrl+o this rewrites
// already-emitted transcript rows, so it forces a full clear+replay
// rather than editing scrollback.
func (i *Interactive) keyCycleToolDisplay(context.Context, tui.Key) keyOutcome {
	i.mu.Lock()
	switch i.view.ToolDisplay {
	case tui.ToolDisplayFull:
		i.view.ToolDisplay = tui.ToolDisplayMinimal
	case tui.ToolDisplayMinimal:
		i.view.ToolDisplay = tui.ToolDisplayGrouped
	case tui.ToolDisplayGrouped:
		i.view.ToolDisplay = tui.ToolDisplayHidden
	default:
		i.view.ToolDisplay = tui.ToolDisplayFull
	}
	i.statusOK = i18n.T("tool display: %s", i.view.ToolDisplay.String())
	i.statusErr = ""
	if i.rend != nil {
		i.rend.Clear()
	}
	i.mu.Unlock()
	i.invalidate()
	return keyHandled
}

// approvalWheel is the shift+tab cycle of approval modes: the three
// everyday postures in increasing autonomy, wrapping back to the start.
// `ask` (confirm every call) and `yolo` (confirm nothing, foreign tools
// included) are deliberately OFF the wheel — reach them through /settings
// or the --approval flag, so a stray keypress can't drop you into the
// strictest testing posture or the most dangerous one.
var approvalWheel = []core.ApprovalMode{core.ApprovalPlan, core.ApprovalWorkspace, core.ApprovalAutoEdit}

// nextApprovalMode returns the wheel entry after cur. A mode that isn't on
// the wheel (ask, yolo, or unset) resolves to the workspace default, so
// shift+tab from any starting posture lands somewhere sensible.
func nextApprovalMode(cur core.ApprovalMode) core.ApprovalMode {
	for idx, m := range approvalWheel {
		if m == cur {
			return approvalWheel[(idx+1)%len(approvalWheel)]
		}
	}
	return core.ApprovalWorkspace
}

// keyCycleApprovalMode (shift+tab) advances the approval mode one step along
// approvalWheel for THIS SESSION only — same posture semantics as the
// /settings picker (SetMode on the daemon-side gate + a tool-set rebuild so
// plan withholds mutating tools), never persisted. Declines (keyPass) when no
// carrier is bound, leaving shift+tab free for its old no-op.
func (i *Interactive) keyCycleApprovalMode(_ context.Context, _ tui.Key) keyOutcome {
	if i.cfg.Carrier == nil {
		return keyPass
	}
	next := nextApprovalMode(core.ApprovalMode(i.approvalModeLabel()))
	i.applyApprovalMode(string(next))
	// Optimistically advance the cached mode so a rapid second shift+tab
	// computes from the value we just set, not a stale one: the carrier's
	// surface_updated round-trip that normally refreshes the cache is async.
	// refreshCarrierApprovalMode confirms (and no-ops if unchanged).
	i.mu.Lock()
	i.carrierApprovalMode = string(next)
	i.mu.Unlock()
	return keyHandled
}

// keyPopQueued (Alt/Option+Up) pops the most recently queued
// ("sliding in") message back into the editor so the user can edit
// and resend it. Repeated presses keep peeling messages off the tail
// of the queue; each press *replaces* the editor contents. When the
// queue is empty the key passes through to the normal Up behavior.
func (i *Interactive) keyPopQueued(context.Context, tui.Key) keyOutcome {
	text, ok := i.popCarrierQueue()
	if !ok || text == "" {
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
