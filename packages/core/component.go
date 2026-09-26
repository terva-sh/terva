package core

import (
	"errors"
	"fmt"
	"reflect"

	"terva.sh/terva/packages/provider"
)

// A component is any value passed to WithComponent. The engine asks it which
// of the capability interfaces below it implements, the way net/http asks a
// ResponseWriter whether it is a Flusher, and connects each one it finds.
// A new hook is therefore a new capability interface: no method is added to
// Agent, and a component written before the hook existed does not change.
//
// A component implements at least one of them. New refuses one that
// implements none, because a component that attaches nothing is almost always
// a value of the wrong type.

// Binder is told the agent it was attached to, once, after every other
// capability of every component is connected. It is where a component keeps
// the agent to read later. Bind must not start a prompt.
type Binder interface {
	Bind(a *Agent)
}

// StepGater contributes a StepGate. Gates run in the order their components
// were passed. A component is the only way to add one.
type StepGater interface {
	StepGate() StepGate
}

// ContinuationGater contributes continuation gates, connected as
// AddContinuationGate would, in the order returned.
type ContinuationGater interface {
	ContinuationGates() []ContinuationGate
}

// GateWrapper wraps the permission gate. Wrappers apply in the order their
// components were passed, so the last one is outermost. A wrapper that
// returns nil is refused: wrapping must not turn a gate into a missing one.
type GateWrapper interface {
	WrapGate(inner Gate) Gate
}

// EventObserver receives every AgentEvent, as AddEventObserver's function
// would.
type EventObserver interface {
	ObserveEvent(ev AgentEvent)
}

// MessageObserver receives every message the loop appends to the transcript,
// as AddMessageObserver's function would.
type MessageObserver interface {
	ObserveMessage(m provider.Message)
}

// DispatchWatcher contributes a DispatchObserver. A component is the only way
// to add one.
type DispatchWatcher interface {
	DispatchObserver() DispatchObserver
}

// QueueObserver hears the messages the loop drains from the queue, as
// AddQueueDrainedObserver's function would.
type QueueObserver interface {
	QueueDrained(drained []string)
}

// TurnFilter can stop a turn before each step.
// At most one component may be a TurnFilter.
type TurnFilter interface {
	BeforeTurn(step int) (allowed bool, reason string)
}

// MessageFilter can refuse or replace a user or an assistant message. The
// transcript keeps a replaced user message as replaced. A replaced assistant
// message changes only what the host is shown, and the transcript keeps the
// model's own words, so the model still sees what it said. At most one
// component may be a MessageFilter.
type MessageFilter interface {
	BeforeUserMessage(text string) (allowed bool, reason, replacement string)
	BeforeAssistantMessage(text string) (allowed bool, reason, replacement string)
}

// A ToolVisibility passed to WithComponent becomes the agent's visibility. It
// is the only way to set one. At most one component may be one.

// errNilComponent and the errors below are what New returns for a component
// it cannot connect.
var errNilComponent = errors.New("WithComponent: component is nil")

// wrapGate applies every GateWrapper among components to gate, in order.
func wrapGate(gate Gate, components []any) (Gate, error) {
	for i, c := range components {
		w, ok := c.(GateWrapper)
		if !ok {
			continue
		}
		gate = w.WrapGate(gate)
		if isNilGate(gate) {
			return nil, fmt.Errorf("component %d (%T): WrapGate returned a nil gate", i, c)
		}
	}
	return gate, nil
}

// checkComponents refuses a component set New cannot connect, before the
// agent exists: a nil or typed-nil component, one pointer passed twice, one
// that implements no capability, and two that claim a capability only one may
// hold. A component passed twice would register each of its hooks twice, so a
// step gate would see every step twice.
func checkComponents(components []any) error {
	var visibility, turn, message []string
	seen := map[any]int{}
	for i, c := range components {
		name := fmt.Sprintf("component %d (%T)", i, c)
		if isNil(c) {
			return fmt.Errorf("%s: %w", name, errNilComponent)
		}
		// Only a pointer has an identity to compare. Two equal values of a
		// value type are indistinguishable, and some are not comparable at all.
		if reflect.ValueOf(c).Kind() == reflect.Pointer {
			if j, dup := seen[c]; dup {
				return fmt.Errorf("%s is component %d passed again; a component attaches once", name, j)
			}
			seen[c] = i
		}
		if !hasCapability(c) {
			return fmt.Errorf("%s implements no capability interface; it would attach nothing", name)
		}
		if _, ok := c.(ToolVisibility); ok {
			visibility = append(visibility, name)
		}
		if _, ok := c.(TurnFilter); ok {
			turn = append(turn, name)
		}
		if _, ok := c.(MessageFilter); ok {
			message = append(message, name)
		}
	}
	for _, only := range []struct {
		what  string
		names []string
	}{{"ToolVisibility", visibility}, {"TurnFilter", turn}, {"MessageFilter", message}} {
		if len(only.names) > 1 {
			return fmt.Errorf("%d components are a %s, and an agent has one: %v", len(only.names), only.what, only.names)
		}
	}
	return nil
}

func hasCapability(c any) bool {
	switch c.(type) {
	case Binder, StepGater, ContinuationGater, GateWrapper, EventObserver,
		MessageObserver, DispatchWatcher, QueueObserver, TurnFilter,
		MessageFilter, ToolVisibility:
		return true
	}
	return false
}

// connect attaches every capability of every component to a, in the order
// the components were passed, then binds them. The gate wrappers were applied
// before the agent was built.
func (a *Agent) connect(components []any) {
	for _, c := range components {
		if g, ok := c.(StepGater); ok {
			a.addStepGate(g.StepGate())
		}
		if g, ok := c.(ContinuationGater); ok {
			for _, gate := range g.ContinuationGates() {
				a.AddContinuationGate(gate)
			}
		}
		if o, ok := c.(EventObserver); ok {
			a.AddEventObserver(o.ObserveEvent)
		}
		if o, ok := c.(MessageObserver); ok {
			a.AddMessageObserver(o.ObserveMessage)
		}
		if o, ok := c.(DispatchWatcher); ok {
			a.addDispatchObserver(o.DispatchObserver())
		}
		if o, ok := c.(QueueObserver); ok {
			a.AddQueueDrainedObserver(o.QueueDrained)
		}
		if f, ok := c.(TurnFilter); ok {
			a.beforeTurn = f.BeforeTurn
		}
		if f, ok := c.(MessageFilter); ok {
			a.beforeUserMessage = f.BeforeUserMessage
			a.beforeAssistantMessage = f.BeforeAssistantMessage
		}
		if v, ok := c.(ToolVisibility); ok {
			a.setToolVisibility(v)
		}
	}
	for _, c := range components {
		if b, ok := c.(Binder); ok {
			b.Bind(a)
		}
	}
}
