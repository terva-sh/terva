package workspace

import (
	"context"

	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/core"
)

// sessionEscalator is the host implementation of core.Escalator: rung 3 of the
// stuck-loop hatch (docs/plans/stuck-loop-escalation-rung3.md). It mirrors
// webAsker — a per-session struct the build funnel binds onto the agent — and
// performs the swap through the same live-switch the /model command uses
// (switchModel), so the transcript is preserved and a local→remote jump is not a
// special case. Bound at buildSession and every rebuildTools, alongside webAsker.
type sessionEscalator struct{ s *wsSession }

// Target reports the configured stronger model, read live so a config edit takes
// effect without rebuilding the session. Both provider and model must be set; a
// partial target is treated as no target, and escalation stays inert.
//
// It reads LoadConfig (the USER layer) deliberately, not the project-overlaid
// view: escalation egresses the transcript to the target provider, so a cloned
// repo's .terva/config.json must never be able to redirect it. ResolveConfig
// overlays only an allowlist that Escalation is not on, so this is belt and
// suspenders — but it is the belt that matters.
func (e *sessionEscalator) Target() (core.EscalationTarget, bool) {
	cfg, _ := config.LoadConfig()
	ec := cfg.Escalation
	if ec == nil || ec.Provider == "" || ec.Model == "" {
		return core.EscalationTarget{}, false
	}
	return core.EscalationTarget{Provider: ec.Provider, Model: ec.Model}, true
}

// Escalate swaps the live session to the configured target through the same
// live-switch the user's /model command uses. Already on the target is a no-op
// (Declined). An unknown model or a missing credential comes back from switchModel
// as an error, which the core turns into "continue on the current model" — a
// failed escalation never fails the turn.
func (e *sessionEscalator) Escalate(_ context.Context, _ core.EscalationRequest) (core.EscalationOutcome, error) {
	tgt, ok := e.Target()
	if !ok {
		return core.EscalationOutcome{Declined: true}, nil
	}
	curProv, curModel := e.s.currentModel()
	if tgt.Provider == curProv && tgt.Model == curModel {
		return core.EscalationOutcome{Declined: true}, nil
	}
	if err := e.s.ws.switchModel(e.s, tgt.Provider, tgt.Model, false); err != nil {
		return core.EscalationOutcome{}, err
	}
	return core.EscalationOutcome{Switched: true, ToProvider: tgt.Provider, ToModel: tgt.Model}, nil
}
