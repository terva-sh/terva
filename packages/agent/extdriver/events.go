package extdriver

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"terva.sh/terva/packages/agent/extproto"
)

// EmitEvent fires a one-way lifecycle event to every extension that
// subscribed to it via SubscribeFromExt.events. It enqueues the frame
// on each subscriber's ordered outbox synchronously, on the calling
// goroutine, so the order events are emitted is the order they reach
// the extension. That FIFO property is what guarantees session_start
// (emitted when the session opens, before any turn) reaches a
// subscriber before that session's first tool_call (the cross-path
// race a per-call goroutine used to lose). The enqueue is non-blocking:
// a wedged extension overflows its buffer and the event is dropped with
// a log, never stalling the agent loop.
//
// Event names are documented on extproto.EventFromHost. Unknown event
// names are still routed (subscribers can use any string they want).
func (d *Driver) EmitEvent(ev extproto.EventFromHost) {
	ev.Type = "event"
	d.mu.RLock()
	subs := make([]*Extension, 0, len(d.ext))
	for _, ext := range d.ext {
		ext.mu.Lock()
		_, subscribed := ext.eventSubs[ev.Event]
		ext.mu.Unlock()
		if subscribed {
			subs = append(subs, ext)
		}
	}
	d.mu.RUnlock()
	if len(subs) == 0 {
		return
	}

	// Encode once; the frame bytes are read-only, so every subscriber's
	// writeLoop can share them.
	frame, err := extproto.Encode(ev)
	if err != nil {
		return
	}
	for _, ext := range subs {
		if ext.outbox == nil {
			continue
		}
		if err := ext.enqueueFrame(frame, false); err == errOutboxFull {
			fmt.Fprintf(ext.logFile, "[terva] dropped %q event: outbox full (extension not keeping up)\n", ev.Event)
		}
	}
}

// InterceptResult aggregates the outcome of walking every subscribed
// interceptor for one event. The zero value means "allow, no
// modification". Callers check Block first; if allowed, use the
// optional rewrite fields (ModifiedArgs for tool_call, ReplaceText
// for assistant_message) to carry the rewrite into the action.
type InterceptResult struct {
	Block        bool
	Reason       string
	ModifiedArgs json.RawMessage
	ReplaceText  string
}

const interceptTimeout = 5 * time.Second

// InterceptToolCall is the typed entry point for tool_call
// interception. Subscribers are invoked serially; the first to return
// Block=true wins. Rewrites (ModifiedArgs) from earlier subscribers
// flow into later ones, so a chain of guards can successively redact
// / patch the args.
func (d *Driver) InterceptToolCall(ctx context.Context, toolID, toolName string, args json.RawMessage) InterceptResult {
	subs := d.interceptSubsFor("tool_call")
	if len(subs) == 0 {
		return InterceptResult{}
	}
	current := args
	for _, ext := range subs {
		r := d.askIntercept(ctx, ext, extproto.EventInterceptFromHost{
			Event:    "tool_call",
			ToolID:   toolID,
			ToolName: toolName,
			ToolArgs: current,
		})
		if r.Block {
			return r
		}
		if len(r.ModifiedArgs) > 0 && json.Valid(r.ModifiedArgs) {
			current = r.ModifiedArgs
		}
	}
	out := InterceptResult{}
	if !jsonEqual(current, args) {
		out.ModifiedArgs = current
	}
	return out
}

// InterceptTurnStart asks every subscriber whether the upcoming turn
// may run. Block=true aborts the turn with Reason shown to the user.
// Rewrites are not supported for this event.
func (d *Driver) InterceptTurnStart(ctx context.Context, step int) InterceptResult {
	subs := d.interceptSubsFor("turn_start")
	if len(subs) == 0 {
		return InterceptResult{}
	}
	for _, ext := range subs {
		r := d.askIntercept(ctx, ext, extproto.EventInterceptFromHost{
			Event: "turn_start",
			Step:  step,
		})
		if r.Block {
			return r
		}
	}
	return InterceptResult{}
}

// InterceptAssistantMessage asks every subscriber to approve, block,
// or rewrite the assistant's final visible text. Block=true hides the
// message from the user entirely; a non-empty ReplaceText rewrites
// what the user sees while keeping the model's original text in the
// transcript. Successive rewrites chain: each subscriber sees the
// previous subscriber's output.
func (d *Driver) InterceptAssistantMessage(ctx context.Context, text string) InterceptResult {
	subs := d.interceptSubsFor("assistant_message")
	if len(subs) == 0 {
		return InterceptResult{}
	}
	current := text
	for _, ext := range subs {
		r := d.askIntercept(ctx, ext, extproto.EventInterceptFromHost{
			Event: "assistant_message",
			Text:  current,
		})
		if r.Block {
			return r
		}
		if r.ReplaceText != "" {
			current = r.ReplaceText
		}
	}
	out := InterceptResult{}
	if current != text {
		out.ReplaceText = current
	}
	return out
}

// interceptSubsFor returns the snapshot of extensions that subscribed
// to intercepting the named event.
func (d *Driver) interceptSubsFor(event string) []*Extension {
	d.mu.RLock()
	defer d.mu.RUnlock()
	subs := make([]*Extension, 0, len(d.ext))
	for _, ext := range d.ext {
		ext.mu.Lock()
		_, subscribed := ext.interceptSubs[event]
		ext.mu.Unlock()
		if subscribed {
			subs = append(subs, ext)
		}
	}
	return subs
}

// askIntercept sends one EventInterceptFromHost to ext and waits for
// the reply, a timeout, or context cancellation. Returns a typed
// result. Never blocks for longer than interceptTimeout.
func (d *Driver) askIntercept(ctx context.Context, ext *Extension, payload extproto.EventInterceptFromHost) InterceptResult {
	id := newCorrelationID()
	ch := make(chan extproto.EventInterceptResponseFromExt, 1)
	ext.mu.Lock()
	ext.pendingIntercept[id] = ch
	ext.mu.Unlock()

	payload.Type = "event_intercept"
	payload.ID = id
	if err := ext.writeFrame(payload); err != nil {
		ext.mu.Lock()
		delete(ext.pendingIntercept, id)
		ext.mu.Unlock()
		fmt.Fprintf(ext.logFile, "[terva] intercept write failed: %v\n", err)
		return InterceptResult{}
	}

	select {
	case resp := <-ch:
		return InterceptResult{
			Block:        resp.Block,
			Reason:       resp.Reason,
			ModifiedArgs: resp.ModifiedArgs,
			ReplaceText:  resp.ReplaceText,
		}
	case <-time.After(interceptTimeout):
		ext.mu.Lock()
		delete(ext.pendingIntercept, id)
		ext.mu.Unlock()
		fmt.Fprintf(ext.logFile, "[terva] intercept %s timed out; allowing\n", payload.Event)
		return InterceptResult{}
	case <-ctx.Done():
		ext.mu.Lock()
		delete(ext.pendingIntercept, id)
		ext.mu.Unlock()
		return InterceptResult{}
	}
}

// jsonEqual reports whether a and b encode the same JSON value. Used
// to detect whether the interceptor chain actually mutated the args.
func jsonEqual(a, b json.RawMessage) bool {
	if len(a) == len(b) {
		same := true
		for i := range a {
			if a[i] != b[i] {
				same = false
				break
			}
		}
		if same {
			return true
		}
	}
	// Fallback to structural compare so whitespace differences don't
	// register as a mutation. (We don't actually rely on this to be
	// cheap, callers only use it to pick a code path.)
	var av, bv any
	if json.Unmarshal(a, &av) != nil || json.Unmarshal(b, &bv) != nil {
		return false
	}
	ae, _ := json.Marshal(av)
	be, _ := json.Marshal(bv)
	return string(ae) == string(be)
}
