package core

import (
	"context"
	"strings"

	"terva.sh/terva/packages/i18n"
)

// Escalation is rung 3 of the stuck-loop hatch (docs/proposals/stuck-loop-escalation.md,
// docs/plans/stuck-loop-escalation-rung3.md): when the stall detector's nudge
// (rung 1) fails to break a loop, hand the live session to a stronger model to
// finish the stuck step — the swap the operator did by hand in the origin session.
//
// Core decides WHEN (the tracker's escalate watermark) and drives consent; the
// HOST owns the swap, because only it can resolve a provider, a credential, and a
// client. The seam mirrors Asker exactly: an interface here, a per-session
// implementation in the workspace, nil in headless modes. Nil Escalator, no
// configured target, a declined offer, or a failed swap all leave the current
// model in place and the turn running — escalation never fails the turn.

// EscalationTarget is the stronger model the host would switch to. Core needs it
// only to name the destination in the consent prompt (local→remote egress is
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

// EscalationDisposition is how an escalation decision resolved. It is recorded so
// a reader of the session log can tell one apart from the others.
type EscalationDisposition string

const (
	// EscalationSwitched: the swap landed and the loop continues on the stronger model.
	EscalationSwitched EscalationDisposition = "switched"
	// EscalationDeclined: the user chose to keep trying, or the host was already on
	// the target — no swap, the turn continues on the current model.
	EscalationDeclined EscalationDisposition = "declined"
	// EscalationStopped: the user chose to end the turn at the offer.
	EscalationStopped EscalationDisposition = "stopped"
	// EscalationFailed: the swap was attempted but errored (no credential, unknown
	// model); non-fatal, the turn continues on the current model.
	EscalationFailed EscalationDisposition = "failed"
)

// EscalationRecord is what an escalation decision produced, handed to escalation
// observers (observers.go). It is the payload hosts persist as an "escalation"
// session row: the swap the hatch performs writes only a "meta" row (via
// UpdateModel), which is byte-identical to a user /model switch — this record is
// the provenance that meta row cannot carry. Emitted only once a target is
// configured (the inert path records nothing), and for every disposition, not
// just successful swaps: a declined or failed escalation is as worth knowing as
// a completed one.
type EscalationRecord struct {
	Reason      string // the stall prose ("stuck on task_update ×5: <error>")
	Tool        string // the tool the model looped on
	FromModel   string // the model in play when the loop was detected (pre-swap)
	ToProvider  string // the configured escalation target
	ToModel     string
	Auto        bool                  // true = swapped under the auto policy, unasked
	Disposition EscalationDisposition // switched | declined | stopped | failed
	Detail      string                // failure error or host note; optional
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

// stallHoldOff is the HOLD-OFF: the detector's second in-band note, a firmer
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
func (a *Agent) stallHoldOff(sig *stallEscalation, sink func(AgentEvent)) {
	if sig == nil {
		return
	}
	a.stall.stageHandoff(stallHoldOffNudge(sig.tool, sig.count, sig.detail))
	rec := StallRecord{Axis: sig.axis, Tool: sig.tool, Detail: sig.detail, Rung: 2}
	a.fireStall(rec)
	sink(EvStall{StallRecord: rec})
}

// stallGiveUp ends the turn when the detector's refusal rung has itself been
// ignored stallRefuseMax times. It reports stop=true, which runLoop turns into a
// clean end of turn — the model keeps its transcript, the refusals are in it, and
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
func (a *Agent) stallGiveUp(sink func(AgentEvent)) bool {
	g, ok := a.stall.gaveUp()
	if !ok {
		return false
	}
	rec := StallRecord{
		Axis: stallAxisSpin,
		Tool: g.tool,
		// The note is the DETAIL rather than a fabricated error slice: this rung
		// has no tool error to quote (the calls stopped running), and a reader of
		// the session row needs the reason the turn ended.
		Detail: stallGiveUpNote(g.tool, g.refusals, g.count),
		Rung:   4,
	}
	a.fireStall(rec)
	sink(EvStall{StallRecord: rec})
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
func (a *Agent) maybeEscalate(ctx context.Context, sink func(AgentEvent)) (stop bool) {
	sig, ok := a.stall.escalation()
	if !ok {
		return false
	}
	a.stall.markEscalated() // one intervention per turn, whichever rung serves it

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
	if !a.stuckLoopEscalationOn() || a.Escalator == nil {
		a.stallHoldOff(sig, sink)
		return false
	}
	target, ok := a.Escalator.Target()
	if !ok {
		a.stallHoldOff(sig, sink) // inert without a configured target
		return false
	}

	// From here a target is configured, so an escalation decision is being made
	// and every terminal path records how it resolved. rec holds the fixed facts;
	// FromModel is read here, before the swap, on the turn goroutine that owns it.
	rec := EscalationRecord{
		Reason:     sig.reason,
		Tool:       sig.tool,
		FromModel:  a.Model,
		ToProvider: target.Provider,
		ToModel:    target.Model,
		Auto:       a.escalateAutoOn(),
	}

	if !rec.Auto {
		asker := a.Asker
		if asker == nil {
			// Nobody to consent and not auto — the unattended case, where a loop
			// runs with no operator watching. The hold-off is the whole intervention
			// available here, which is exactly when it matters most.
			a.stallHoldOff(sig, sink)
			return false
		}
		escalateOpt := i18n.T("Escalate")
		stopOpt := i18n.T("Stop")
		ans, err := AskOne(ctx, asker, UserQuestion{
			Question: i18n.T(
				"The model appears stuck (%s). Escalate to %s (%s) to finish this step? This sends the conversation to that provider.",
				sig.reason, target.Model, target.Provider),
			Options: []string{escalateOpt, i18n.T("Keep trying"), stopOpt},
		})
		if err != nil {
			return false // couldn't ask: no decision was made
		}
		if ans.Answer == stopOpt {
			a.recordEscalation(sink, rec, EscalationStopped, "")
			return true // user chose to end the turn
		}
		if ans.Declined || ans.Answer != escalateOpt {
			a.recordEscalation(sink, rec, EscalationDeclined, "")
			// "Keep trying": give the model a fresh window and re-arm escalation
			// (with backoff) instead of suppressing it for the rest of the run.
			a.stall.forgive()
			return false // keep trying on the current model
		}
	}

	out, err := a.Escalator.Escalate(ctx, EscalationRequest{
		Reason:    sig.reason,
		Tool:      sig.tool,
		Signature: sig.signature,
	})
	if err != nil {
		a.recordEscalation(sink, rec, EscalationFailed, err.Error())
		return false // non-fatal: continue on the current model
	}
	if !out.Switched {
		a.recordEscalation(sink, rec, EscalationDeclined, out.Note) // e.g. already on the target
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
	a.recordEscalation(sink, rec, EscalationSwitched, out.Note)
	a.stall.stageHandoff(handoffMarker(out.ToModel, sig.reason))
	// The strikes belonged to the model that just left. Clearing them keeps the
	// stronger model from inheriting a turn that was one refusal from ending —
	// escalating exists to give the step another chance, and it would not be one.
	a.stall.pardon()
	return false
}

// recordEscalation stamps the disposition and detail onto rec, fires it to the
// escalation observers (which persist it as a session row — observers.go,
// wiring) AND emits it as a live EvEscalation on sink (UI + extension observers).
// The two are separate delivery paths for the same fact: durable record vs
// in-the-moment signal. Detail is bounded — a swap failure carries a provider
// error that must not bloat the session file.
func (a *Agent) recordEscalation(sink func(AgentEvent), rec EscalationRecord, d EscalationDisposition, detail string) {
	rec.Disposition = d
	rec.Detail = clip(strings.TrimSpace(detail), escalationDetailMax)
	a.fireEscalation(rec)
	sink(EvEscalation{EscalationRecord: rec})
}

// escalationDetailMax bounds the recorded failure/note text.
const escalationDetailMax = 200

func handoffMarker(toModel, reason string) string {
	return i18n.P("stall.handoff",
		"[handoff] The previous model was %s. `%s` now takes this step. The failed attempts of the previous model are above. Complete the pending work, then continue normally.",
		reason, toModel)
}

// SetStuckLoopEscalation toggles rung 3 (engine feature stuck_loop_escalation).
// Off at the core zero value; inert without a bound Escalator and a configured
// target, and it depends on the detector being on (that is the trigger).
func (a *Agent) SetStuckLoopEscalation(on bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.stuckLoopEscalate = on
}

// StuckLoopEscalationEnabled reports whether rung 3 is armed. Exported so the
// build funnel's default is testable — the only place the shipped default lives.
func (a *Agent) StuckLoopEscalationEnabled() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.stuckLoopEscalate
}

func (a *Agent) stuckLoopEscalationOn() bool { return a.StuckLoopEscalationEnabled() }

// SetEscalateAuto toggles unasked escalation (config escalation.auto). Off by
// default: the user is asked before a swap that egresses their transcript.
func (a *Agent) SetEscalateAuto(on bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.escalateAuto = on
}

// EscalateAutoEnabled reports the auto-escalate policy. Exported so the build
// funnel's config wiring is testable, like the feature toggles.
func (a *Agent) EscalateAutoEnabled() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.escalateAuto
}

func (a *Agent) escalateAutoOn() bool { return a.EscalateAutoEnabled() }
