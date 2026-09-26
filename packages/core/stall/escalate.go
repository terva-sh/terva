package stall

import (
	"context"
	"strings"

	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/core/i18n"
)

// Escalation is rung 3 of the stuck-loop hatch (docs/proposals/stuck-loop-escalation.md,
// docs/plans/stuck-loop-escalation-rung3.md): when the stall detector's nudge
// (rung 1) fails to break a loop, hand the live session to a stronger model to
// finish the stuck step — the swap the operator did by hand in the origin session.
//
// The detector decides WHEN (the tracker's escalate watermark) and drives
// consent; the HOST owns the swap, because only it can resolve a provider, a
// credential, and a client. The seam mirrors core.Asker: an interface here, a
// per-session implementation in the workspace, nil in headless modes. Nil Escalator, no
// configured target, a declined offer, or a failed swap all leave the current
// model in place and the turn running — escalation never fails the turn.

// EscalationTarget is the stronger model the host would switch to. The detector
// needs it only to name the destination in the consent prompt (local→remote egress is
// real, and the user is told where their transcript is going).
type EscalationTarget struct {
	Provider string
	Model    string
}

// EscalationRequest describes the stall that triggered the offer.
type EscalationRequest struct {
	Reason    string // "stuck on task_update ×5: <error>"
	Tool      string
	Signature string // opaque; lets the host rate-limit / de-dup across turns
}

// EscalationOutcome is what the host did. Switched drives the handoff marker;
// Declined (no target, or already on it) and a returned error are both non-fatal.
type EscalationOutcome struct {
	Switched   bool
	ToProvider string
	ToModel    string
	Declined   bool
	Note       string
}

// Escalator swaps the live agent to a stronger model, preserving the transcript.
// Implementations must honor ctx and must never fail the turn: return a Declined
// outcome or an error, and the caller keeps the current model.
type Escalator interface {
	// Target reports the configured stronger model (for the consent prompt) and
	// whether one is configured at all. No target → escalation is inert.
	Target() (EscalationTarget, bool)
	// Escalate performs the swap and reports what it did.
	Escalate(ctx context.Context, r EscalationRequest) (EscalationOutcome, error)
}

// holdOff is the HOLD-OFF: the detector's second in-band note, a firmer
// word that fires when a loop has outlived the first one and the hatch's later
// rungs cannot act.
//
// Deliberately not called "rung 2" — the proposal's ladder already gives that
// number to the human ask. This is a second note on rung 1's own channel, which
// is why it needs no Asker, no target and no consent.
//
// It exists because the ladder used to terminate at rung 1 for any deployment
// without an escalation target — which is every deployment until someone
// configures one. The tracker would establish that a signature had crossed the
// watermark, `maybeEscalate` would find nothing to escalate to, and the loop
// would run on unremarked; in the session that produced TW-028, ten further
// identical calls followed the single nudge inside one turn.
//
// This is deliberately the SMALLEST thing that closes that: no new threshold
// (it rides stallEscalateAfterNudge, already the watermark), no new
// configuration, and no refusal to dispatch. terva still does not decide on the
// model's behalf — it just stops being silent. markEscalated has already fired
// by the time this is called, so it speaks at most once per turn.
//
// The record carries Rung 2 — the count of in-band notes, not a hatch rung — so
// a later reader can tell this from the first nudge, which is what makes "did
// the second word land?" answerable from the session log the way the first
// already is.
func (d *Detector) holdOff(sig *stallEscalation, sink func(core.AgentEvent)) {
	if sig == nil {
		return
	}
	d.mu.Lock()
	d.t.stageHandoff(stallHoldOffNudge(d.t.tr, sig.tool, sig.count, sig.detail))
	d.mu.Unlock()
	rec := core.StallRecord{Axis: sig.axis, Tool: sig.tool, Detail: sig.detail, Rung: 2}
	sink(core.EvStall{StallRecord: rec})
}

// giveUp ends the turn when the detector's refusal rung has itself been
// ignored stallRefuseMax times. It reports stop=true, which the step gate hands
// the engine as a clean end of turn — the model keeps its transcript, the refusals are in it, and
// the user is handed back control with a note saying why.
//
// This is the one place terva decides FOR the model, and the bar is deliberately
// high: a call has to have returned the same result seven times, survived two
// in-band notes, and then been re-issued three more times after the harness
// started answering it without running it. Everything short of that leaves the
// turn running.
//
// Nothing is appended to the transcript beyond the refusals already in it. A
// synthetic assistant message would be a claim the model did not make, and the
// refused tool results say the same thing more honestly to whoever resumes.
func (d *Detector) giveUp(sink func(core.AgentEvent)) bool {
	d.mu.Lock()
	g, ok := d.t.gaveUp()
	tr := d.t.tr
	d.mu.Unlock()
	if !ok {
		return false
	}
	rec := core.StallRecord{
		Axis: stallAxisSpin,
		Tool: g.tool,
		// The note is the DETAIL rather than a fabricated error slice: this rung
		// has no tool error to quote (the calls stopped running), and a reader of
		// the session row needs the reason the turn ended.
		Detail: stallGiveUpNote(tr, g.tool, g.refusals, g.count),
		Rung:   4,
	}
	sink(core.EvStall{StallRecord: rec})
	return true
}

// maybeEscalate acts on a raised escalation request: under the auto policy it
// swaps directly, otherwise it asks the user first. It returns stop=true only
// when the user explicitly chooses to end the turn. Everything else — a swap, a
// decline, a failure, no target, no channel — returns false and lets the loop
// continue on whatever model is now active.
//
// Requires the detector to be running (the trigger comes from observe), the
// escalation feature on, and an Escalator bound. In headless modes with no Asker
// and auto off, there is no one to consent, so the nudge simply stood.
func (d *Detector) maybeEscalate(ctx context.Context, sink func(core.AgentEvent)) (stop bool) {
	d.mu.Lock()
	sig, ok := d.t.escalation()
	if ok {
		d.t.markEscalated() // one intervention per turn, whichever rung serves it
	}
	escalate, esc, auto, a, tr := d.escalate, d.escalator, d.auto, d.agent, d.t.tr
	d.mu.Unlock()
	if !ok {
		return false
	}

	// Rung 3 needs three things: the feature on, an Escalator bound, and a target
	// configured. Every one of them missing is the DEFAULT state rather than an
	// edge case — no deployment has an escalation target until someone sets one —
	// so each of these was a path where the tracker had already established that a
	// loop crossed the watermark and then nothing whatsoever happened. That is the
	// gap TW-028 named: with escalation unconfigured there was no rung between one
	// nudge and silence. They fall through to the hold-off now.
	//
	// Note the gate: the hold-off is part of the DETECTOR, not of escalation. A
	// deployment that turned stuck_loop_escalation off asked not to have its model
	// swapped; it did not ask to stop being told it is looping.
	if !escalate || esc == nil {
		d.holdOff(sig, sink)
		return false
	}
	target, ok := esc.Target()
	if !ok {
		d.holdOff(sig, sink) // inert without a configured target
		return false
	}

	// From here a target is configured, so an escalation decision is being made
	// and every terminal path records how it resolved. rec holds the fixed facts;
	// FromModel is read here, before the swap, on the turn goroutine that owns it.
	rec := core.EscalationRecord{
		Reason:     sig.reason,
		Tool:       sig.tool,
		ToProvider: target.Provider,
		ToModel:    target.Model,
		Auto:       auto,
	}
	var asker core.Asker
	if a != nil {
		rec.FromModel, asker = a.Model(), a.Asker()
	}

	if !rec.Auto {
		if asker == nil {
			// Nobody to consent and not auto — the unattended case, where a loop
			// runs with no operator watching. The hold-off is the whole intervention
			// available here, which is exactly when it matters most.
			d.holdOff(sig, sink)
			return false
		}
		escalateOpt := i18n.In(tr).T("Escalate")
		stopOpt := i18n.In(tr).T("Stop")
		ans, err := core.AskOne(ctx, asker, core.UserQuestion{
			Question: i18n.In(tr).T(
				"The model appears stuck (%s). Escalate to %s (%s) to finish this step? This sends the conversation to that provider.",
				sig.reason, target.Model, target.Provider),
			Options: []string{escalateOpt, i18n.In(tr).T("Keep trying"), stopOpt},
		})
		if err != nil {
			return false // couldn't ask: no decision was made
		}
		if ans.Answer == stopOpt {
			recordEscalation(sink, rec, core.EscalationStopped, "")
			return true // user chose to end the turn
		}
		if ans.Declined || ans.Answer != escalateOpt {
			recordEscalation(sink, rec, core.EscalationDeclined, "")
			// "Keep trying": give the model a fresh window and re-arm escalation
			// (with backoff) instead of suppressing it for the rest of the run.
			d.mu.Lock()
			d.t.forgive()
			d.mu.Unlock()
			return false // keep trying on the current model
		}
	}

	out, err := esc.Escalate(ctx, EscalationRequest{
		Reason:    sig.reason,
		Tool:      sig.tool,
		Signature: sig.signature,
	})
	if err != nil {
		recordEscalation(sink, rec, core.EscalationFailed, err.Error())
		return false // non-fatal: continue on the current model
	}
	if !out.Switched {
		recordEscalation(sink, rec, core.EscalationDeclined, out.Note) // e.g. already on the target
		return false
	}

	// The swap landed. Prefer the outcome's reported destination (the host may
	// resolve it more precisely than the configured target), then record it and
	// stage a one-turn handoff marker so the incoming model knows why it is here
	// and completes the pending step (it recovers by reading the loop it
	// inherited — the marker makes that explicit).
	if out.ToProvider != "" {
		rec.ToProvider = out.ToProvider
	}
	if out.ToModel != "" {
		rec.ToModel = out.ToModel
	}
	recordEscalation(sink, rec, core.EscalationSwitched, out.Note)
	d.mu.Lock()
	defer d.mu.Unlock()
	d.t.stageHandoff(handoffMarker(d.t.tr, out.ToModel, sig.reason))
	// The strikes belonged to the model that just left. Clearing them keeps the
	// stronger model from inheriting a turn that was one refusal from ending —
	// escalating exists to give the step another chance, and it would not be one.
	d.t.pardon()
	return false
}

// recordEscalation stamps the disposition and detail onto rec and emits it as
// EvEscalation on sink. That one event is both the durable record and the live
// signal: the event observers see it first, and AttachTranscriptStore writes
// the session row from it, then the sink carries it to clients. Detail is bounded — a swap failure carries a provider
// error that must not bloat the session file.
func recordEscalation(sink func(core.AgentEvent), rec core.EscalationRecord, disp core.EscalationDisposition, detail string) {
	rec.Disposition = disp
	rec.Detail = clip(strings.TrimSpace(detail), escalationDetailMax)
	sink(core.EvEscalation{EscalationRecord: rec})
}

// escalationDetailMax bounds the recorded failure/note text.
const escalationDetailMax = 200

func handoffMarker(tr i18n.Translator, toModel, reason string) string {
	return i18n.In(tr).P("stall.handoff",
		"[handoff] The previous model was %s. `%s` now takes this step. The failed attempts of the previous model are above. Complete the pending work, then continue normally.",
		reason, toModel)
}
