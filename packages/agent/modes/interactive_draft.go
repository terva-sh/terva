package modes

// The composer draft, persisted — stage 5 of
// docs/proposals/session-state-sidecar.md.
//
// What you typed and did not send survives a restart, and belongs to the
// SESSION rather than to this terminal: the daemon keeps it beside the
// transcript (sessions.state / sessions.set_composer), so the web panel and an
// attached TUI see the same unsent text instead of each holding a private copy.
//
// Two rules shape everything below.
//
// THE EDITOR IS MAIN-LOOP-ONLY STATE. packages/tui.Editor has no mutex and
// Editor.Render reads it on the render goroutine, so every read and write here
// either runs on the main loop already (the animation tick, the key path) or
// marshals with runOnMain. i.mu does NOT protect the editor. That is also why
// the off-loop savers (session switch, exit) read a MIRROR under i.mu rather
// than the editor itself: a draft is not worth a data race.
//
// A SUGGESTION IS NOT THE USER'S WRITING. An unaccepted offer persists too, but
// tagged, and it comes back as an offer. Handing the machine's line back as the
// user's own prose is the one failure this feature must not produce.

import (
	"context"
	"errors"
	"strings"
	"time"

	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/i18n"
)

// composerDraftDebounce is how long the composer must sit unchanged before the
// draft is written.
//
// A debounce rather than a save per keystroke: the write is a whole-file
// round trip through the daemon, and typing a paragraph would otherwise be a
// few hundred of them. Short enough that a crash loses a word rather than a
// thought, long enough that ordinary typing produces one write.
const composerDraftDebounce = 800 * time.Millisecond

// stateCarrier returns the carrier's session-state controller and the bound
// session, or ok=false when this front end has neither — an unbound TUI, a
// replay carrier, or any service that does not serve the verbs.
func (i *Interactive) stateCarrier() (ctrlproto.SessionStateController, string, bool) {
	if i.cfg.Carrier == nil {
		return nil, "", false
	}
	sc, ok := i.cfg.Carrier.(ctrlproto.SessionStateController)
	if !ok {
		return nil, "", false
	}
	sess := i.carrierSession()
	if sess == "" {
		return nil, "", false
	}
	return sc, sess, true
}

// composerContents reads what is in the composer and where it came from.
//
// MAIN LOOP ONLY — it reads the editor.
//
// SubmitValue rather than Value: the raw buffer holds placeholder markers like
// "[pasted text #1]" that point into a map this process owns, so persisting it
// would store a reference that means nothing after a restart.
//
// A standing offer counts when the composer is otherwise empty. It is the
// user's next likely move and losing it on a restart is a small loss, but it
// travels TAGGED so the restore can put it back as an offer.
func (i *Interactive) composerContents() (text, source string) {
	if v := i.ed.SubmitValue(); strings.TrimSpace(v) != "" {
		return v, ctrlproto.ComposerSourceUser
	}
	if g := i.ed.Ghost(); i.ed.GhostVisible() && strings.TrimSpace(g) != "" {
		return g, ctrlproto.ComposerSourceSuggestion
	}
	return "", ""
}

// noteComposerDraft refreshes the mirror the off-loop savers read.
//
// MAIN LOOP ONLY. Called from the key path (so the mirror is exact the instant
// a key is processed, which is what makes the save on exit lose nothing) and
// from the animation tick (so an offer arriving without a keystroke is
// noticed).
func (i *Interactive) noteComposerDraft() {
	text, source := i.composerContents()
	i.mu.Lock()
	if text != i.composerDraft || source != i.composerDraftSource {
		i.composerDraft, i.composerDraftSource = text, source
		i.composerDraftAt = time.Now()
	}
	i.mu.Unlock()
}

// maybeSaveComposerDraft rides the animation tick, next to the idle offer and
// for the same reason: nothing here needs 120ms precision, and a tick that
// already runs is cheaper than a timer every editing path would have to arm and
// cancel.
//
// MAIN LOOP ONLY (noteComposerDraft reads the editor).
func (i *Interactive) maybeSaveComposerDraft() {
	i.noteComposerDraft()

	sc, sess, ok := i.stateCarrier()
	if !ok {
		return
	}
	i.mu.Lock()
	text, source := i.composerDraft, i.composerDraftSource
	unchanged := text == i.composerDraftSaved && source == i.composerDraftSavedSource
	settled := time.Since(i.composerDraftAt) >= composerDraftDebounce
	if unchanged || !settled || i.composerDraftInFlight {
		i.mu.Unlock()
		return
	}
	// Claim the write BEFORE it goes out. Claiming it on the way back would let
	// the next tick start a second write while the first is still in flight —
	// 120ms apart, for as long as the round trip takes — and two writes racing
	// to the same slot can land in the wrong order.
	i.composerDraftInFlight = true
	i.composerDraftSaved, i.composerDraftSavedSource = text, source
	i.mu.Unlock()

	go func() {
		err := sc.SetComposerDraft(context.Background(), sess, ctrlproto.ComposerDraft{Text: text, Source: source})
		i.runOnMain(func() {
			i.mu.Lock()
			i.composerDraftInFlight = false
			if err != nil && !isDraftTooLarge(err) {
				// A transient failure un-claims the write, so the next tick
				// tries again. An over-cap one does NOT: it can never succeed,
				// and retrying it every 800ms would be a stutter the user
				// cannot stop.
				i.composerDraftSaved, i.composerDraftSavedSource = "", ""
			}
			i.mu.Unlock()
			if err != nil && isDraftTooLarge(err) {
				// The one failure worth interrupting for. Silence here would be
				// a lie by omission: the user would find out their draft was
				// not kept by losing it.
				i.statusErr = i18n.T("draft too long to keep — it will not survive a restart")
				i.invalidate()
			}
		})
	}()
}

// isDraftTooLarge reports whether err is the daemon refusing an over-cap draft,
// which is the one save failure the user can act on (shorten it, or send it).
func isDraftTooLarge(err error) bool {
	var wire *ctrlproto.Error
	if !errors.As(err, &wire) {
		return false
	}
	return wire.Code == ctrlproto.CodeBadRequest
}

// flushComposerDraft writes the mirror now, for the two paths that have no next
// tick to wait for: leaving a session, and leaving the program.
//
// It reads the MIRROR and never the editor, because both callers run off the
// main loop. It is synchronous: an asynchronous flush on the way out is a
// goroutine racing process exit, which is how a feature that promises not to
// lose your writing loses your writing.
//
// sess is passed rather than resolved, so the switch path can name the session
// being LEFT — by the time it matters, cfg.CarrierSession may already be the
// new one.
func (i *Interactive) flushComposerDraft(sess string) {
	if i.cfg.Carrier == nil || sess == "" {
		return
	}
	sc, ok := i.cfg.Carrier.(ctrlproto.SessionStateController)
	if !ok {
		return
	}
	i.mu.Lock()
	text, source := i.composerDraft, i.composerDraftSource
	unchanged := text == i.composerDraftSaved && source == i.composerDraftSavedSource
	if !unchanged {
		i.composerDraftSaved, i.composerDraftSavedSource = text, source
	}
	i.mu.Unlock()
	if unchanged {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), draftFlushTimeout)
	defer cancel()
	// Best-effort by design: this runs while the user is leaving, and there is
	// nowhere left to show an error that they would still be looking at.
	_ = sc.SetComposerDraft(ctx, sess, ctrlproto.ComposerDraft{Text: text, Source: source})
}

// draftFlushTimeout bounds the flush on the way out. An attached TUI talks to a
// daemon over a socket that may be gone; without a bound, quitting would hang
// on a draft.
const draftFlushTimeout = 2 * time.Second

// dropComposerDraftMirror forgets the mirror when the composer stops belonging
// to the session it was typed in. Called on a session switch AFTER the flush:
// carrying the old thread's text into the new binding would let the next tick
// save one session's draft into another's slot.
func (i *Interactive) dropComposerDraftMirror() {
	i.mu.Lock()
	i.composerDraft, i.composerDraftSource = "", ""
	i.composerDraftSaved, i.composerDraftSavedSource = "", ""
	i.composerDraftAt = time.Time{}
	i.mu.Unlock()
}

// restoreComposerDraft asks for the new binding's draft and puts it back.
//
// One-shot per binding, armed by armCarrierBind and spent here — a compact or a
// clear sends a snapshot too, and neither is a new conversation whose draft
// needs fetching. The same shape as retractOfferOnBind, next to which it runs.
func (i *Interactive) restoreComposerDraft() {
	i.mu.Lock()
	armed := i.carrierDraftArmed
	i.carrierDraftArmed = false
	i.mu.Unlock()
	if !armed {
		return
	}
	sc, sess, ok := i.stateCarrier()
	if !ok {
		return
	}
	go func() {
		st, err := sc.SessionState(context.Background(), sess)
		if err != nil || st.Composer == nil || strings.TrimSpace(st.Composer.Text) == "" {
			// No draft is the ordinary answer, and a failed read is not worth a
			// banner: nothing the user did is waiting on it.
			return
		}
		draft := *st.Composer
		// The editor is main-loop-only state and this is a pump goroutine, so
		// the write goes onto the loop. Pinned by
		// TestTheDraftRestoreIsMarshalledOntoTheMainLoop, because -race cannot
		// see a violation here.
		i.runOnMain(func() { i.applyRestoredDraft(sess, draft) })
	}()
}

// applyRestoredDraft puts a fetched draft into the composer.
//
// MAIN LOOP ONLY.
//
// It lands as an OFFER, not as text, in either of two cases:
//
//   - the draft is a suggestion. It was never the user's writing, and restoring
//     it as text would quietly convert the machine's line into their own.
//   - the composer is not empty. The read took a round trip, and whatever the
//     user has typed since outranks what they typed before: overwriting live
//     keystrokes with older ones is the one way this feature could cost writing
//     rather than save it.
//
// An offer is refusable — one keystroke dismisses it — and that asymmetry is
// the point: guessing wrong costs the user nothing.
func (i *Interactive) applyRestoredDraft(sess string, d ctrlproto.ComposerDraft) {
	// The binding may have moved on while the read was in flight. Restoring
	// then would put one session's draft into another session's composer.
	if i.carrierSession() != sess {
		return
	}
	if d.Source == ctrlproto.ComposerSourceSuggestion || !i.ed.IsEmpty() {
		i.ed.SetGhost(d.Text)
	} else {
		i.ed.SetValue(d.Text)
	}
	// Seed the mirror AND the saved marker: the daemon already holds exactly
	// this, so the next tick has nothing to write. Without the saved marker the
	// restore would immediately echo itself back over the wire.
	text, source := i.composerContents()
	i.mu.Lock()
	i.composerDraft, i.composerDraftSource = text, source
	i.composerDraftSaved, i.composerDraftSavedSource = text, source
	i.composerDraftAt = time.Now()
	i.mu.Unlock()
	i.invalidate()
}
