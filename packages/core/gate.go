package core

import (
	"context"
	"encoding/json"
	"reflect"

	"terva.sh/terva/packages/provider"
)

// Gate decides whether a tool call runs. The engine asks it once per call,
// after the tool has been resolved and its arguments parsed, and before the
// tool executes. New requires one, so an agent cannot be built without a
// decision about who approves its tool calls.
//
// Core defines the question and never answers it. terva's own answer is the
// ladder in build.BuildToolGate: user hooks, then the permission policy and
// its confirmer, then extension intercepts.
type Gate interface {
	// CheckTool returns allowed=false with a reason to refuse the call. The
	// model sees the reason as the tool's error result, so write it for the
	// model. A non-nil modifiedArgs that is valid JSON replaces the arguments
	// the tool receives, and the transcript keeps the model's original.
	//
	// tool is the implementation this call will run, taken from the registry
	// pinned for the turn. It lets a gate describe the call, for example
	// through an optional Preview method, without a handle on the agent.
	//
	// ctx is the turn's. A gate that waits on a person or a process must stop
	// when ctx does. If ctx ends while the gate waits, the engine discards an
	// allow that arrives late and does not run the tool.
	CheckTool(ctx context.Context, call provider.ToolCallBlock, tool Tool) (allowed bool, reason string, modifiedArgs json.RawMessage)
}

// GateFunc adapts a function to the Gate interface.
type GateFunc func(ctx context.Context, call provider.ToolCallBlock, tool Tool) (allowed bool, reason string, modifiedArgs json.RawMessage)

// CheckTool calls f.
func (f GateFunc) CheckTool(ctx context.Context, call provider.ToolCallBlock, tool Tool) (bool, string, json.RawMessage) {
	return f(ctx, call, tool)
}

// AllowAll is the gate that allows every tool call and changes nothing.
//
// It gives up every check a gate exists to make. No permission rule or
// approval mode applies, no hook runs, no person is asked, no extension can
// refuse or rewrite a call, and no audit line is written. Pass it only when
// you have decided that everything the model asks for may run. A host that
// has not decided wants a real gate.
//
// Its type is unexported, so the value cannot be reassigned to nil.
var AllowAll allowAll

type allowAll struct{}

// CheckTool allows the call.
func (allowAll) CheckTool(context.Context, provider.ToolCallBlock, Tool) (bool, string, json.RawMessage) {
	return true, "", nil
}

// noGateReason is the refusal an Agent built without New gives every
// tool call. A zero Agent has no gate, and running its calls unchecked is the
// failure New's required gate exists to prevent.
const noGateReason = "tool call refused: this agent has no permission gate. Build it with core.New, which requires one."

// isNilGate reports whether g cannot answer a call: a nil interface, or a
// non-nil interface holding a nil value. The second case is the one a
// `g == nil` check misses. A nil GateFunc, or a nil pointer, map, or channel
// whose type implements Gate, passes that check and then panics on the first
// tool call, inside a turn, instead of at the line that made the mistake.
//
// A pointer type whose CheckTool deliberately handles a nil receiver is
// refused too. That shape is legal Go, but a permission gate is the wrong
// place for it, and refusing it keeps the rule to one sentence: a gate must be
// a non-nil value.
func isNilGate(g Gate) bool { return isNil(g) }

// isNil reports whether v is nil or a typed nil of a kind that can be one. A
// component is held to the same rule as a gate: a typed nil passes `v == nil`
// and then panics in the first method New calls on it.
func isNil(v any) bool {
	if v == nil {
		return true
	}
	switch rv := reflect.ValueOf(v); rv.Kind() {
	case reflect.Func, reflect.Pointer, reflect.Map, reflect.Chan, reflect.Slice, reflect.Interface:
		return rv.IsNil()
	}
	return false
}
