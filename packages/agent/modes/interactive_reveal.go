package modes

import (
	"context"

	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/i18n"
	"terva.sh/terva/packages/provider"
)

// Compaction scrollback for the TUI. See docs/proposals/scrollback-history.md.
//
// A compaction is a checkpoint, not a deletion: the turns it folded away are still
// in the session file. conversation.reveal reads them back, and the TUI prepends
// them to what it paints — display-only, never into carrierMessages, which is the
// wire twin of what the model actually sees.
//
// The trigger is the one the TUI already teaches: scroll to the top and older
// content appears. View.TailLimit has always worked that way; this is the same
// gesture, continued past the point where the transcript itself runs out.

// displayTranscript is what the chat paints: the turns paged back in, then the live
// transcript. The two never overlap — the daemon subtracts the tail each checkpoint
// kept, and a Go test pins it — so this is a concatenation, not a merge.
func (i *Interactive) displayTranscript() []provider.Message {
	i.mu.Lock()
	defer i.mu.Unlock()
	if len(i.revealed) == 0 {
		return i.carrierTranscriptLocked()
	}
	out := make([]provider.Message, 0, len(i.revealed)+len(i.carrierMessages))
	out = append(out, i.revealed...)
	out = append(out, i.carrierMessages...)
	return out
}

// clearDivider is the display-only message that marks where a /clear was. A clear is
// an EMPTY checkpoint, so it leaves nothing in the transcript to hang a boundary on —
// without this, history paged in from before one would simply run out mid-air.
//
// state is "true" while the clear still stands between you and what came before it,
// and "crossed" once you have said /reveal and looked anyway. Either way it stays on
// screen: the point is to show WHERE the conversation was cut, not to hide it.
//
// It is never sent anywhere. It lives in Interactive.revealed, which is display-only
// and deliberately separate from carrierMessages (the wire twin of what the model
// sees), so this cannot leak into a request.
func clearDivider(state string) provider.Message {
	return provider.Message{
		Role: provider.RoleUser,
		Meta: map[string]string{core.MetaClear: state},
	}
}

// compactionSummaryText returns the text of msgs[0] when it is a compaction summary
// — the divider the reveal chain hangs off — and "" when it is not.
func compactionSummaryText(msgs []provider.Message) string {
	if len(msgs) == 0 || msgs[0].Meta[core.MetaCompaction] != "true" {
		return ""
	}
	for _, c := range msgs[0].Content {
		if tb, ok := c.(provider.TextBlock); ok {
			return tb.Text
		}
	}
	return ""
}

// resetRevealForTranscript re-validates the paged-in history against a snapshot.
//
// A snapshot replaces the transcript wholesale and lands at the end of EVERY turn,
// not just on open — so without this, a user would page in an hour of history, send
// one message, and watch it vanish. Keep it when the live divider is still the same
// checkpoint (same summary text, which the checkpoint itself wrote); drop it when it
// is not, because auto-compact fires at turn end — exactly when a snapshot arrives —
// and history hung off the old checkpoint does not belong under the new one.
//
// Caller holds mu.
func (i *Interactive) resetRevealForTranscript(msgs []provider.Message) {
	anchor := compactionSummaryText(msgs)
	if anchor != "" && anchor == i.revealAnchor {
		return // same checkpoint; what the user paged in still belongs here
	}
	i.revealed = nil
	i.revealAnchor = anchor
	i.revealNext = -1 // the live divider is always the LATEST checkpoint
	i.revealClear = false
	i.revealDone = anchor == "" // no divider, nothing to reveal
}

// wantsReveal reports whether there is more history to fetch and now is the time.
//
// Whether anything remains is the DAEMON's answer (revealDone comes from the
// PrevOrdinal it reported), not something re-derived from what happens to be at the
// top of the screen. That distinction is load-bearing: crossing a /clear exposes a
// span with no summary message at its head, because a clear is an empty checkpoint
// and leaves none — so a head-shaped test would decide, wrongly, that the
// conversation had ended there.
//
// Caller holds mu.
func (i *Interactive) wantsReveal() bool {
	return !i.revealDone && !i.revealBusy && !i.revealClear && i.cfg.Carrier != nil
}

// startReveal pages in the next span of history, in the background: the carrier call
// is a round trip (a network one, under `terva attach`) and the render loop must not
// wait on it. Re-entry is guarded by revealBusy.
func (i *Interactive) startReveal() {
	i.mu.Lock()
	if !i.wantsReveal() {
		i.mu.Unlock()
		return
	}
	i.revealBusy = true
	// cfg.CarrierSession read directly, not through carrierSession(), which takes mu.
	ordinal, sess, carrier := i.revealNext, i.cfg.CarrierSession, i.cfg.Carrier
	i.mu.Unlock()

	go func() {
		res, err := carrier.Reveal(context.Background(), sess, ordinal)

		i.mu.Lock()
		i.revealBusy = false
		if err != nil {
			// An ephemeral session has no file to read back, and an older daemon has
			// no such method. Neither is the user's fault, and neither is worth
			// nagging about on every scroll — stop asking.
			i.revealDone = true
			i.mu.Unlock()
			i.setStatusErr(i18n.T("earlier turns are not available: %s", err.Error()))
			i.invalidate()
			return
		}
		older := make([]provider.Message, 0, len(res.Messages))
		for _, m := range res.Messages {
			older = append(older, core.MessageFromWire(m))
		}
		// This span came from crossing a clear, so the marker standing for that clear
		// — the topmost thing we hold — is now behind us. Leave it where it is (it
		// still marks the spot) but stop it offering a crossing already made.
		if res.Clear && len(i.revealed) > 0 && i.revealed[0].Meta[core.MetaClear] == "true" {
			i.revealed[0] = clearDivider("crossed")
		}
		// Prepend: the span sits ABOVE the divider it came from.
		i.revealed = append(older, i.revealed...)
		// A clear behind this checkpoint leaves NO summary message to draw a boundary
		// on — it is an empty checkpoint. Mint the divider, or the conversation above
		// simply stops with nothing to say why.
		if res.PrevClear {
			i.revealed = append([]provider.Message{clearDivider("true")}, i.revealed...)
		}
		i.revealNext = res.PrevOrdinal
		i.revealClear = res.PrevClear
		// The daemon says when the conversation runs out, and only it can: a clear
		// leaves no summary message behind, so there is nothing at the head of the
		// span to infer "no more history" from.
		i.revealDone = res.PrevOrdinal < 0
		i.carrierMessagesRev++ // the chat render cache keys off this
		stopped, crossed := i.revealClear, res.Clear
		i.mu.Unlock()

		switch {
		case stopped:
			// The walk has run into a clear. The divider says so too, but the status
			// line is where the TUI answers "why did scrolling stop?".
			i.setStatusOK(i18n.T("the conversation was cleared here — /reveal to show what came before"))
		case crossed:
			// Replace that hint. Leaving it up would keep advising a crossing the user
			// has already made.
			i.setStatusOK(i18n.T("showing the conversation from before the clear"))
		}
		i.invalidate()
	}()
}

// revealAcrossClear crosses a /clear, on purpose.
//
// The automatic walk stops at one: a clear is a deliberate act — "done with that,
// start fresh" — nearer a session boundary than a compaction, which merely condenses
// a conversation you are still having. So undoing it takes a deliberate act too, and
// this is it (the /reveal command).
//
// It is not an unlock. Those turns were never inaccessible: they are in the session
// file in plaintext, and --replay raw, export, and session_inspect all read them.
// This only declines to put them on screen until asked.
func (i *Interactive) revealAcrossClear() {
	i.mu.Lock()
	if i.cfg.Carrier == nil || i.revealBusy || !i.revealClear {
		i.mu.Unlock()
		i.setStatusErr(i18n.T("nothing to reveal — the conversation was not cleared above here"))
		return
	}
	// The clear's own ordinal is what the daemon serves the crossing for; clearing
	// the flag is what re-arms the ordinary walk beyond it.
	i.revealClear = false
	i.revealDone = false
	i.mu.Unlock()
	i.startReveal()
}
