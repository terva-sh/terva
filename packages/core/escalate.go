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
	if !a.stuckLoopEscalationOn() || a.Escalator == nil {
		return false
	}
	sig, ok := a.stall.escalation()
	if !ok {
		return false
	}
	a.stall.markEscalated() // one offer per turn, whatever the outcome

	target, ok := a.Escalator.Target()
	if !ok {
		return false // inert without a configured target
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
			return false // no channel to consent and not auto: the nudge stood, no decision made
		}
		escalateOpt := i18n.T("Escalate")
		stopOpt := i18n.T("Stop")
		ans, err := asker.Ask(ctx, UserQuestion{
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
		"[handoff] The previous model was %s and has been replaced with `%s` for this step. Its failing attempts are above — complete the pending work, then continue normally.",
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
