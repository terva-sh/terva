package modes

// The idle trigger — stage 3 of docs/proposals/idle-suggestions.md.
//
// After the agent finishes a reply and the user goes quiet at an empty
// composer, ask the daemon once for a line they might type next and offer it as
// ghost text. The two halves it joins already exist: the daemon's one-shot ask
// (workspace_nextstep.go) and the editor's unaccepted offer (tui/editor.go).
//
// ONCE PER REPLY, not on a timer. Arming is what expires — the moment the ask
// goes out the arm is spent, so a user who walks away for an hour costs one
// completion rather than a hundred and twenty. A new reply arms it again.
//
// Silence alone is not the trigger. Every suppression below answers a way this
// could be wrong rather than merely noisy, and they are checked at FIRE time
// rather than at arm time, because all of them can change during the wait.

import (
	"context"
	"time"

	"terva.sh/terva/packages/agent/ctrlproto"
)

// nextStepIdle is how long the user must be quiet, with a reply finished and an
// empty composer, before terva asks what they might do next.
//
// Long enough to read a reply and start typing without being interrupted;
// short enough that it still lands while the user is looking at the answer
// rather than minutes after they have moved on.
const nextStepIdle = 30 * time.Second

// noteUserActivity restarts the silence. Called from the key path on every
// keystroke, including the ones that do nothing: a user hammering an
// unrecognised key is present, and being asked "what next?" while they are at
// the keyboard is the wrong read of the room.
func (i *Interactive) noteUserActivity() {
	i.mu.Lock()
	i.lastInputAt = time.Now()
	i.mu.Unlock()
}

// armNextStep starts the idle window after a reply lands. Refuses when the turn
// ended badly: a cancelled turn means the user pressed Esc, and answering that
// with "here is what to do next" ignores what they just said; a failed turn
// has no reply to build on.
func (i *Interactive) armNextStep() {
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.nextStepTurnBad {
		return
	}
	i.nextStepArmedAt = time.Now()
}

// spoilNextStep marks the running turn as one that must not produce an offer,
// and drops any arm already standing. Reached from the error and cancel paths.
func (i *Interactive) spoilNextStep() {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.spoilNextStepLocked()
}

// spoilNextStepLocked is spoilNextStep for callers already holding i.mu.
// handleWireEvent holds it across its whole body, so the cancel branch there
// must use this one — the locking twin deadlocked the pump the first time.
func (i *Interactive) spoilNextStepLocked() {
	i.nextStepTurnBad = true
	i.nextStepArmedAt = time.Time{}
}

// freshNextStepTurn clears the bad-turn mark as a new turn begins, so one
// failure does not suppress every suggestion for the rest of the session.
func (i *Interactive) freshNextStepTurn() {
	i.mu.Lock()
	i.nextStepTurnBad = false
	i.nextStepArmedAt = time.Time{}
	i.mu.Unlock()
}

// maybeOfferNextStep runs on the animation tick and decides whether to ask.
// Main-loop only: it reads the editor and the overlay registry, which are not
// mutex-guarded.
func (i *Interactive) maybeOfferNextStep() {
	if !i.nextStepReady() {
		return
	}
	sc, sess, ok := i.nextStepCarrier()
	if !ok {
		return
	}
	// Spend the arm HERE, before the ask goes out rather than after it comes
	// back. Spending it on the answer would let the next tick fire a second ask
	// while the first is still in flight — 120ms apart, for as long as the
	// model takes.
	i.mu.Lock()
	i.nextStepArmedAt = time.Time{}
	i.nextStepInFlight = true
	i.mu.Unlock()

	go func() {
		res, err := sc.SuggestNextStep(context.Background(), sess)
		i.runOnMain(func() {
			i.mu.Lock()
			i.nextStepInFlight = false
			i.mu.Unlock()
			if err != nil || res.Line == "" {
				// No suggestion is an ordinary answer and a failed one is not
				// worth a banner: the user did not ask for this, and telling
				// them it did not work would be more interruption than the
				// feature was ever going to save them.
				return
			}
			// Re-check on the way in. The completion took real time, and the
			// user may have started typing, sent something, or opened a dialog
			// while it ran — in which case the composer is no longer theirs to
			// offer into.
			if !i.nextStepOfferable() {
				return
			}
			i.ed.SetGhost(res.Line)
			i.invalidate()
		})
	}()
}

// nextStepReady reports whether the idle window has elapsed with nothing
// suppressing the ask.
func (i *Interactive) nextStepReady() bool {
	i.mu.Lock()
	on, armed, inFlight := i.nextStepEnabled, i.nextStepArmedAt, i.nextStepInFlight
	last := i.lastInputAt
	i.mu.Unlock()

	if !on || armed.IsZero() || inFlight {
		return false
	}
	now := time.Now()
	if now.Sub(armed) < nextStepIdle {
		return false
	}
	// The silence is measured from the LAST of the two. Typing and then
	// deleting resets it, which is the point: that user is at the keyboard.
	if !last.IsZero() && now.Sub(last) < nextStepIdle {
		return false
	}
	return i.nextStepOfferable()
}

// nextStepOfferable reports whether the composer is currently something we may
// put an offer into. Checked before asking AND again before showing, because
// the ask takes long enough for every one of these to change underneath it.
func (i *Interactive) nextStepOfferable() bool {
	if i.turns.Busy() {
		// A turn is running, so the answer to "what next" is being written.
		return false
	}
	i.mu.Lock()
	shell := i.shellRunning
	i.mu.Unlock()
	if shell {
		// A `!` command is running in this process; the user is mid-errand.
		return false
	}
	if !i.ed.IsEmpty() {
		// They are already writing. Their words outrank the machine's idea, and
		// an offer is only ever drawn on an empty composer anyway.
		return false
	}
	if i.activeOverlay() != nil {
		// A dialog owns the bottom of the screen. The composer underneath is
		// not what the user is looking at.
		return false
	}
	return true
}

// nextStepCarrier resolves the daemon-side surface, or reports that this build
// cannot ask. An attached client whose carrier predates the verb simply never
// offers, rather than erroring at the user about a feature they did not invoke.
func (i *Interactive) nextStepCarrier() (ctrlproto.NextStepController, string, bool) {
	if i.cfg.Carrier == nil {
		return nil, "", false
	}
	sc, ok := i.cfg.Carrier.(ctrlproto.NextStepController)
	if !ok {
		return nil, "", false
	}
	sess := i.carrierSession()
	if sess == "" {
		return nil, "", false
	}
	return sc, sess, true
}
