package core

import (
	"context"

	"terva.sh/terva/packages/provider"
)

// StepGate is a component's hook on the turn loop's tool steps: told when a
// prompt's loop begins, told after each batch of tool calls has run, and able
// to end the turn there. It is the seam the stuck-loop detector
// (packages/core/stall) attaches through, and the one precedent for its shape
// is ContinuationGate, which may extend a turn at its close. Seam B of
// docs/plans/stall-component.md.
//
// Every hook is optional. A gate runs on the turn goroutine, except Inner (see
// below), and must not call back into the loop that is running it.
type StepGate struct {
	// Cause labels the gate in code and telemetry ("stall"). A label, not an
	// identity.
	Cause string
	// Begin is called once at the start of each prompt's loop, before its first
	// request: a Prompt, a ContinueAssistant, a regenerate. It is where a gate
	// resets state scoped to one prompt.
	Begin func()
	// After is called when a batch of tool calls has run, with the assistant
	// message that asked for them and the message carrying their results. emit
	// sends an event as the engine's own are sent: to the event observers, which
	// is how a store writes it (AttachTranscriptStore), and then to the
	// prompt's sink. stop true ends the turn cleanly, as a finished answer
	// would; the gates after it are not called.
	After func(ctx context.Context, s StepState, emit func(AgentEvent)) (stop bool)
	// Inner is called when a tool reports a host tool it called on its own
	// behalf (ReportInnerCall), with the model-issued call it was running for.
	// It reaches the gates of the prompt the tool runs in, the same set whose
	// Begin and After run, so a gate never hears of an inner call from a step
	// its After will not see.
	// It runs on whatever goroutine the tool does, so a gate that keeps inner
	// calls guards them itself. What to keep, and how much, is the gate's: the
	// engine stores nothing, so a script making thousands of host calls costs
	// only what the gate chooses to hold.
	Inner func(outerCallID, tool string, res ToolResult)
}

// StepState is one tool step as After sees it.
type StepState struct {
	// Assistant is the message whose tool calls ran; Results is the message
	// that answers them, one ToolResultBlock per call, matched by CallID.
	Assistant, Results provider.Message
}

// addStepGate registers a step gate. Gates are consulted in registration
// order, and the first whose After answers stop ends the turn. The set is
// snapshotted when a prompt's loop begins, so a gate added mid-turn takes
// part, in all three hooks, from the next prompt. A gate with no hooks is a
// no-op.
func (a *Agent) addStepGate(g StepGate) {
	if g.Begin == nil && g.After == nil && g.Inner == nil {
		return
	}
	a.obsMu.Lock()
	a.stepGates = append(a.stepGates, g)
	a.obsMu.Unlock()
}

func (a *Agent) stepGateSnapshot() []StepGate {
	a.obsMu.RLock()
	defer a.obsMu.RUnlock()
	if len(a.stepGates) == 0 {
		return nil
	}
	return append([]StepGate(nil), a.stepGates...)
}

// stepGatesKey carries a prompt's step-gate snapshot to the tools it runs, for
// ReportInnerCall.
type stepGatesKey struct{}

func stepGatesFromContext(ctx context.Context) []StepGate {
	g, _ := ctx.Value(stepGatesKey{}).([]StepGate)
	return g
}

// beginStep calls every gate's Begin.
func beginStep(gates []StepGate) {
	for _, g := range gates {
		if g.Begin != nil {
			g.Begin()
		}
	}
}

// afterStep asks the gates in order, and reports whether one ended the turn.
func afterStep(ctx context.Context, gates []StepGate, s StepState, emit func(AgentEvent)) (stop bool) {
	for _, g := range gates {
		if g.After != nil && g.After(ctx, s, emit) {
			return true
		}
	}
	return false
}

// ReportInnerCall reports the outcome of a host tool that another tool called
// on its own behalf, against the model-issued call currently executing. It
// hands the report to every step gate's Inner; the engine keeps nothing.
//
// Called from the single script→host crossing (tools.dispatchHostTool). It is
// a no-op unless the context came from a model-issued dispatch, so a direct
// call, a test dispatch, or an extension's host_tool_call attributes nothing.
func ReportInnerCall(ctx context.Context, tool string, res ToolResult) {
	outer := outerCallFromContext(ctx)
	if outer == "" {
		return
	}
	for _, g := range stepGatesFromContext(ctx) {
		if g.Inner != nil {
			g.Inner(outer, tool, res)
		}
	}
}

// outerCallKey tags a dispatch context with the id of the model-issued call
// being executed, so a tool that calls back into the host can attribute the
// inner call to the outer one. Only set by runOneTool: a call arriving through
// any other door (an extension's host_tool_call, a direct dispatch in a test)
// carries no outer id and is therefore not attributed to anything, which is
// correct — no model-issued step exists to fold it into.
type outerCallKey struct{}

func contextWithOuterCall(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, outerCallKey{}, id)
}

func outerCallFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	id, _ := ctx.Value(outerCallKey{}).(string)
	return id
}
