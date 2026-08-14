package modes

// Key handling outside the overlay registry and global keymap: the
// handleKey pipeline, input history, the ctrl+c exit arm, and path
// tab-completion.

import (
	"context"
	"strings"
	"time"

	"terva.sh/terva/packages/agent/modes/widgets"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/i18n"
	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/tui"
)

// ctrlCExitWindow is how long after a ctrl+c press a *second* press
// will exit instead of just clearing input. Long enough to be
// deliberate (rules out accidental key chord), short enough that the
// hint stays meaningful.
const ctrlCExitWindow = 2 * time.Second

// armCtrlCExit records the timestamp of the current ctrl+c so the next
// one within ctrlCExitWindow exits.
func (i *Interactive) armCtrlCExit() {
	i.mu.Lock()
	i.lastCtrlC = time.Now()
	i.mu.Unlock()
}

// ctrlCExitArmed reports whether a previous ctrl+c was recent enough
// that another press should now exit.
func (i *Interactive) ctrlCExitArmed() bool {
	i.mu.Lock()
	t := i.lastCtrlC
	i.mu.Unlock()
	return !t.IsZero() && time.Since(t) <= ctrlCExitWindow
}

// clearFileSuggestQuery strips the filter the user typed after the
// last "@", leaving the bare "@" so the picker stays open. Called when
// navigating between directory levels (Right/Left): the filter applied
// to the level the user was on, not the one being entered, so carrying
// it forward would wrongly hide the new directory's contents.
func (i *Interactive) clearFileSuggestQuery() {
	val := i.ed.Value()
	if idx := strings.LastIndex(val, "@"); idx >= 0 {
		i.ed.SetValue(val[:idx+1])
	}
}

func (i *Interactive) handleKey(ctx context.Context, k tui.Key) (done bool) {
	// Every key, before any branch can consume it: the idle window is about
	// whether the user is AT the keyboard, so a keystroke that turns out to be
	// a no-op still counts as presence.
	i.noteUserActivity()

	// Re-evaluate the draft-stash nudge on the way out of every key,
	// whichever branch consumed it — the arming condition (a bit of
	// draft typed while a turn is in flight) and the disarming ones
	// (draft emptied, stash taken) are both reached from many paths.
	defer i.refreshStashHint()

	// Any key that isn't ctrl+c invalidates an armed ctrl+c-exit, so
	// pressing ctrl+c then typing then ctrl+c much later doesn't quit
	// unexpectedly. The hint message also goes stale; clear it.
	if k.Kind != tui.KeyCtrlC {
		i.mu.Lock()
		if !i.lastCtrlC.IsZero() {
			i.lastCtrlC = time.Time{}
			if strings.HasPrefix(i.statusOK, "input cleared") || strings.HasPrefix(i.statusOK, "press ctrl+c") {
				i.statusOK = ""
			}
		}
		i.mu.Unlock()
	}

	// Dialogs consume keys while open (except ctrl+c, which closes
	// them — see each entry's ctrlC in overlay_registry.go). The
	// registry is priority-ordered; the confirm dialog sits first
	// because the agent goroutine is blocked waiting for its answer,
	// so no key may leak anywhere else while it's up.
	if handled, done := i.dispatchOverlayKey(k); handled {
		return done
	}

	// Replay transport: in `terva replay` mode the playback keys
	// (space / arrows / speed) drive the recording's playhead and take
	// priority over the editor. Non-transport keys fall through.
	if i.handleReplayKey(ctx, k) {
		return false
	}

	// Global keys: one named binding per chord in keymap.go. A
	// binding may decline (keyPass) — e.g. Esc while a popup is open,
	// Alt+Up with an empty queue — and the key continues to the
	// downstream consumers below.
	if handled, done := i.dispatchGlobalKey(ctx, k); handled {
		return done
	}

	// Note: we intentionally do NOT gate the editor on busy state.
	// Typing while the agent is working is supported — submitted
	// messages are queued and delivered as follow-up turns when the
	// current turn ends. See the submit handler below.

	// Slash suggestions: intercept up/down/tab/enter when the popup is visible.
	if i.suggest.Active(i.ed.Value()) {
		switch k.Kind {
		case tui.KeyUp:
			i.suggest.Up()
			return false
		case tui.KeyDown:
			i.suggest.Down()
			return false
		case tui.KeyPageUp:
			i.suggest.PageUp()
			return false
		case tui.KeyPageDown:
			i.suggest.PageDown()
			return false
		case tui.KeyTab:
			if name := i.suggest.Selection(i.ed.Value()); name != "" {
				i.ed.SetValue(name)
				i.suggest.Reset()
			}
			return false
		case tui.KeyEnter:
			// Enter on an ambiguous or partial slash prefix: complete to the
			// currently highlighted command and run it. That way typing
			// "/lo" + enter picks whichever of /login or /logout is selected
			// in the popup instead of submitting "/lo" as unknown. Also
			// clear the editor so the command doesn't linger after the
			// dialog opens/closes.
			if name := i.suggest.Selection(i.ed.Value()); name != "" {
				i.ed.Clear()
				i.suggest.Reset()
				// This is the second way a slash command runs from the
				// keyboard (the submit branch below is the other), so
				// recall has to be fed here too — and with the completed
				// name, which is what the user would want to re-run, not
				// the "/lo" prefix they actually typed.
				i.recordInput(name, false)
				i.inputHistoryIndex = -1
				return i.runSlash(ctx, name)
			}
		case tui.KeyEsc:
			i.ed.Clear()
			i.suggest.Reset()
			return false
		}
	}

	// File suggestions: intercept up/down/tab/enter when the @-popup is visible.
	if i.fileSuggest.Active(i.ed.Value()) {
		switch k.Kind {
		case tui.KeyUp:
			i.fileSuggest.Up()
			return false
		case tui.KeyDown:
			i.fileSuggest.Down()
			return false
		case tui.KeyRight:
			// Open selected directory. The filter the user typed picked
			// that directory at the current level; once we descend it no
			// longer applies to the directory's contents, so clear it.
			// Otherwise typing "@eda" then right would re-filter inside
			// eda/ by "eda" and show nothing.
			if i.fileSuggest.Right() {
				i.clearFileSuggestQuery()
			}
			return false
		case tui.KeyLeft:
			// Go back to parent directory. Clear the filter for the same
			// reason as Right: it was scoped to the level we just left.
			if i.fileSuggest.Left() {
				i.clearFileSuggestQuery()
			}
			return false
		case tui.KeyTab:
			// Shell-style completion of the @-token in place: extend to the
			// unique candidate or the longest common prefix (AtComplete —
			// the web composer runs the same fixture-pinned semantics).
			// Enter stays the commit; Tab only rewrites text.
			if nv, ok := i.fileSuggest.TabComplete(i.ed.Value()); ok {
				i.ed.SetValue(nv)
			}
			return false
		case tui.KeyEnter:
			if entry, ok := i.fileSuggest.SelectedEntry(i.ed.Value()); ok {
				var chip string
				if entry.IsDir {
					chip = "[dir:" + entry.Rel + "/]"
				} else {
					chip = "[file:" + entry.Rel + "]"
				}
				val := i.ed.Value()
				if idx := strings.LastIndex(val, "@"); idx >= 0 {
					val = val[:idx]
				}
				i.ed.SetValue(val + chip + " ")
				i.fileSuggest.Reset()
			}
			return false
		case tui.KeyEsc:
			val := i.ed.Value()
			if idx := strings.LastIndex(val, "@"); idx >= 0 {
				i.ed.SetValue(val[:idx])
			}
			i.fileSuggest.Reset()
			return false
		}
	}

	// Tab-complete a path token in the editor when no popup is open.
	// Recognises tokens that look like paths (start with ~, /, ./, ../
	// or contain a slash); shell-style completion expands ~, lists the
	// parent dir, and completes the basename to the longest common
	// prefix. Single match: full replace and trailing / for dirs.
	// No match: no-op. Plain bare words (no slash, no tilde) fall
	// through so Tab keeps its current no-op behaviour outside paths.
	if k.Kind == tui.KeyTab && !i.suggest.Active(i.ed.Value()) && !i.fileSuggest.Active(i.ed.Value()) {
		if i.tryPathTabComplete() {
			return false
		}
	}

	if i.handleInputHistoryKey(k) {
		return false
	}
	if i.inputHistoryIndex >= 0 && k.Kind != tui.KeyLeft && k.Kind != tui.KeyRight {
		i.inputHistoryIndex = -1
	}

	if submit := i.ed.HandleKey(k); submit {
		// SubmitValue() expands any [pasted text #N +L lines]
		// placeholders back into their bodies; the raw Value()
		// is only what the user sees on screen.
		text := strings.TrimRight(i.ed.SubmitValue(), "\n")
		// Expand [file:name] and [dir:name/] chips to full paths.
		text = widgets.ExpandFileChips(text, i.cfg.CWD)
		// Reconcile any clipboard images pasted this turn: markers still in
		// the text attach their image, deleted markers drop theirs. Only
		// rewrites the text when something was actually pasted.
		var clipImages []provider.ImageBlock
		// Snapshot what the user is actually looking at before any of it is
		// consumed: SubmitValue expanded the paste placeholders, the reconcile
		// below empties the pending images, and Clear takes the rest. A prompt
		// withdrawn a moment from now is restored from THIS, not from the
		// expanded string on the wire (see withdrawableDraft).
		preSubmitEd, preSubmitImages := i.ed.State(), i.clipboardImages
		if len(i.clipboardImages) > 0 {
			text, clipImages = preparePromptWithClipboardImages(text, i.clipboardImages)
			i.clipboardImages = nil
		}
		if text == "" && len(clipImages) == 0 {
			return false
		}
		i.ed.Clear()
		i.inputHistoryIndex = -1
		i.suggest.Reset()
		i.fileSuggest.Reset()

		if cmd, ok := shellEscapeCommand(text); ok {
			i.recordInput(text, false)
			i.startShellEscape(ctx, cmd)
			return false
		}

		if looksLikeSlashCommand(text) {
			head := text
			rest := ""
			if idx := strings.IndexAny(text, " \t"); idx >= 0 {
				head = text[:idx]
				rest = strings.TrimSpace(text[idx:])
			}
			// Recorded before the known/unknown split so a mistyped
			// command is recallable and fixable rather than lost with
			// the editor the submit just cleared.
			i.recordInput(text, false)
			if !isKnownSlashCommand(text) {
				// Try extensions before giving up. Extensions register
				// commands by bare name (no leading slash); strip it here.
				extName := strings.TrimPrefix(head, "/")
				if i.cfg.Extensions != nil && i.cfg.Extensions.HasCommand(extName) {
					go i.invokeExtensionCommand(ctx, extName, rest)
					return false
				}
				i.mu.Lock()
				i.statusErr = i18n.T("unknown command %s — type /help to see the list", head)
				i.statusOK = ""
				i.mu.Unlock()
				return false
			}
			// Slash commands run regardless of busy state. Commands that
			// would mutate the transcript or replace the agent (/clear,
			// /compact, /logout, /login, /model) cancel the active turn
			// first and wait for the goroutine to wind down so they don't
			// race with a streaming response. Safe commands (/help,
			// /jump, /sessions, /jail, /unjail, /exit) run immediately
			// without disturbing the active turn.
			if slashCancelsTurn(head) {
				i.cancelAndWaitForIdle()
			}
			return i.runSlash(ctx, text)
		}

		if !i.ready() {
			// Logged as out-of-thread: it never became a turn, so it
			// will never appear in the transcript, but the user still
			// wants the text back after logging in.
			i.recordInput(text, false)
			i.setStatusErr("not logged in. type /login first.")
			return false
		}
		i.recordInput(text, true)
		// Armed only here, past the slash-command and shell-escape returns:
		// those never become a prompt, so nothing about them can be withdrawn.
		i.armWithdrawableDraft(preSubmitEd, preSubmitImages, text)
		// The chat mirror lives daemon-side: Workspace.Prompt echoes a
		// client-originated prompt into the paired chat, so every client
		// mirrors, not just this keyboard.
		//
		// startTurn claims-or-queues atomically inside the turn
		// engine: if a turn is already in flight the prompt queues
		// for the agent loop's next safe model-call boundary.
		if len(clipImages) > 0 {
			i.startTurnWithImages(ctx, text, clipImages)
		} else {
			i.startTurn(ctx, text)
		}
		// The message that displaced a parked draft is on its way
		// (sent, or queued for the turn boundary) — bring the draft
		// back so the user resumes where they left off.
		i.popStashedDraft()
	}
	return false
}

func (i *Interactive) handleInputHistoryKey(k tui.Key) bool {
	if k.Kind != tui.KeyLeft && k.Kind != tui.KeyRight {
		return false
	}
	// Do not steal normal cursor movement. History browsing can only
	// start from an empty editor; once active, Left/Right keep walking
	// the ring so repeated presses work even though the editor now
	// contains the selected historical prompt.
	if i.inputHistoryIndex < 0 && !i.ed.IsEmpty() {
		return false
	}
	hist := i.inputHistory()
	if len(hist) == 0 {
		return false
	}

	if i.inputHistoryIndex < 0 {
		// Start just after the newest item so Left lands on the most
		// recent user prompt and Right keeps the editor empty.
		i.inputHistoryIndex = len(hist)
	}

	switch k.Kind {
	case tui.KeyLeft:
		if i.inputHistoryIndex > 0 {
			i.inputHistoryIndex--
		}
	case tui.KeyRight:
		if i.inputHistoryIndex < len(hist) {
			i.inputHistoryIndex++
		}
	}

	if i.inputHistoryIndex >= len(hist) {
		i.ed.Clear()
	} else {
		i.ed.SetValue(hist[i.inputHistoryIndex])
	}
	return true
}

// inputLogEntry is one thing the user submitted from this keyboard.
// inThread marks the submissions that became a turn, and so also show
// up in the transcript — the join key inputHistory() uses to avoid
// listing this session's prompts twice.
type inputLogEntry struct {
	text     string
	inThread bool
}

// maxInputLog caps the per-session log. Deep enough that recall covers
// a long working session, bounded so a scripted or very long-lived
// session can't grow it without limit.
const maxInputLog = 500

// recordInput appends a submission to the recall log. Consecutive
// duplicates collapse, shell-history style, so holding enter on the
// same command doesn't bury everything else.
//
// Locked because SwitchCarrierSession clears the log, and it runs on the
// session-load goroutine, not the main loop.
func (i *Interactive) recordInput(text string, inThread bool) {
	if strings.TrimSpace(text) == "" {
		return
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	if n := len(i.inputLog); n > 0 && i.inputLog[n-1].text == text {
		// Keep the newer entry's inThread: a command re-run as a prompt
		// (or vice versa) should count the way it landed this time.
		i.inputLog[n-1].inThread = inThread
		return
	}
	i.inputLog = append(i.inputLog, inputLogEntry{text: text, inThread: inThread})
	if len(i.inputLog) > maxInputLog {
		i.inputLog = append(i.inputLog[:0], i.inputLog[len(i.inputLog)-maxInputLog:]...)
	}
}

// inputHistory returns what Left/Right walks: the prompts already in
// the transcript, followed by everything typed this session.
//
// The two sources overlap — a prompt sent this session is in both — so
// the transcript is truncated by the number of in-thread submissions
// the log holds, and the log supplies that tail instead. That is what
// puts `!` escapes and `/slash` commands back in the position they were
// actually typed, rather than lumped at one end. The transcript still
// owns everything from before this process started (a resumed session,
// or a prompt another client sent), which the log cannot know about.
//
// Truncation is clamped: /clear empties the transcript while the log
// keeps its entries, and recall staying useful across a /clear is the
// behaviour we want anyway. A session SWITCH is the case that does not
// survive — SwitchCarrierSession empties the log, because the join it
// anchors is to one thread's tail and means nothing against another's.
func (i *Interactive) inputHistory() []string {
	// carrierTranscript takes mu, so read the transcript before locking
	// for the log — mu is a plain Mutex and does not re-enter.
	msgs := i.carrierTranscript()
	cold := make([]string, 0, len(msgs))
	for _, m := range msgs {
		if m.Role != provider.RoleUser || core.IsToolImageMirror(m) {
			continue
		}
		text := userMessageText(m)
		if strings.TrimSpace(text) == "" {
			continue
		}
		cold = append(cold, text)
	}
	i.mu.Lock()
	log := append([]inputLogEntry(nil), i.inputLog...)
	i.mu.Unlock()

	mine := 0
	for _, e := range log {
		if e.inThread {
			mine++
		}
	}
	if mine > len(cold) {
		mine = len(cold)
	}
	hist := make([]string, 0, len(cold)-mine+len(log))
	hist = append(hist, cold[:len(cold)-mine]...)
	for _, e := range log {
		hist = append(hist, e.text)
	}
	return hist
}

func userMessageText(m provider.Message) string {
	var sb strings.Builder
	for _, c := range m.Content {
		if tb, ok := c.(provider.TextBlock); ok {
			if sb.Len() > 0 {
				sb.WriteByte('\n')
			}
			sb.WriteString(tb.Text)
		}
	}
	return sb.String()
}

// invokeExtensionCommand fires an extension-registered slash command
// in a background goroutine, awaits the response, and applies the
// requested action (prompt / insert / display / noop). Errors and
// timeouts surface as a status_err line.

// tryPathTabComplete is the Interactive-bound convenience wrapper.
// It calls the free helper against the main editor and invalidates
// the frame on a successful rewrite.
func (i *Interactive) tryPathTabComplete() bool {
	if widgets.TryPathTabCompleteEditor(i.ed, i.cfg.CWD) {
		i.invalidate()
		return true
	}
	return false
}
