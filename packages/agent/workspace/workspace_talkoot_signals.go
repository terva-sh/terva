package workspace

import (
	"errors"
	"strings"
	"sync"

	"terva.sh/terva/packages/agent/swarm"
	"terva.sh/terva/packages/agent/talkoot"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/provider"
)

// A member's signals reach the room here (talkoot/signals.go): a tool call
// that failed, a provider retry, and a card that opened or closed. A failed
// turn needs nothing here, because its turn line carries the failure.

// nativeSignals turns a native member's session events into signal lines. It
// keeps the name of each tool call the turn runs, because a result carries
// only the call's id.
type nativeSignals struct {
	mu    sync.Mutex
	calls map[string]string
	// refused holds the calls whose permission card a person refused or that
	// closed unanswered. The gate returns the refusal as a failed result, and
	// the card_close line already records it.
	refused map[string]bool
}

// refuse marks call id as refused at its permission card, so its failed
// result writes no tool_error line.
func (n *nativeSignals) refuse(id string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.refused == nil {
		n.refused = map[string]bool{}
	}
	n.refused[id] = true
}

// observe is an event observer of the session sessID.
//
// 🔑 It records on the turn's goroutine and waits for the seat, as a read
// does (talkootRead). A turn then writes its signals before its turn line,
// which the same goroutine writes at the turn's end. Only a tool result that
// failed and a retry reach the router, so a text delta costs one type switch.
func (n *nativeSignals) observe(w *Workspace, sessID string, ev core.AgentEvent) {
	switch e := ev.(type) {
	case core.EvToolCall:
		n.mu.Lock()
		if n.calls == nil {
			n.calls = map[string]string{}
		}
		n.calls[e.ID] = e.Name
		n.mu.Unlock()
	case core.EvToolResult:
		n.mu.Lock()
		name := n.calls[e.ID]
		refused := n.refused[e.ID]
		delete(n.calls, e.ID)
		delete(n.refused, e.ID)
		n.mu.Unlock()
		if e.Result.IsError && !refused {
			why := failureReason(toolErrorText(e.Result.Content))
			w.talkootSignal(sessID, func(rt *talkoot.Router, member string) error {
				return rt.ToolFailed(member, e.ID, name, why)
			})
		}
	case core.EvRetry:
		why := failureReason(e.Err)
		w.talkootSignal(sessID, func(rt *talkoot.Router, member string) error {
			return rt.Retried(member, e.Attempt, why)
		})
	case core.EvDone:
		n.mu.Lock()
		n.calls, n.refused = nil, nil
		n.mu.Unlock()
	}
}

// toolErrorText joins the text of a failed tool result.
func toolErrorText(content []provider.Content) string {
	var parts []string
	for _, c := range content {
		if t, ok := c.(provider.TextBlock); ok && t.Text != "" {
			parts = append(parts, t.Text)
		}
	}
	return strings.Join(parts, " ")
}

// talkootSignal records a signal for the member whose seat session sessID
// holds. A session with no seat, or a revoked or retired one, records
// nothing.
func (w *Workspace) talkootSignal(sessID string, fn func(rt *talkoot.Router, member string) error) {
	if w == nil {
		return
	}
	w.talkoot.mu.Lock()
	b := w.talkoot.seats[sessID]
	w.talkoot.mu.Unlock()
	if b == nil {
		return
	}
	if err := (talkootSeat{b: b, w: w}).signal(fn); err != nil && !errors.Is(err, ErrTalkootClosed) {
		w.diagf("talkoot: session %s could not record a signal: %v", sessID, err)
	}
}

// signal runs fn with the router and the seat's member, as read does.
func (s talkootSeat) signal(fn func(rt *talkoot.Router, member string) error) error {
	s.b.mu.RLock()
	defer s.b.mu.RUnlock()
	if s.b.revoked {
		return nil
	}
	return s.b.run.do(func(rt *talkoot.Router) error {
		if s.b.retired.Load() {
			return nil
		}
		return fn(rt, s.b.member)
	})
}

// talkootCard records that card c opened, or with an outcome that it closed,
// in the run the card counted in. A card that opened outside a seat records
// nothing. id is the card's call id or ask id, and tool the tool call a
// permission card asks about.
//
// ⚠️ The line goes through the run's queue, so the caller does not wait for
// the run. A worker's approval opens while an update can hold the run. So a
// card line can land after the answer line or the turn line that followed it.
func (w *Workspace) talkootCard(c openCard, card, id, tool, outcome string) {
	if w == nil || c.run == nil || c.member == "" {
		return
	}
	run := c.run
	run.later(func() {
		err := run.do(func(rt *talkoot.Router) error {
			if outcome == "" {
				return rt.CardOpened(c.member, card, id, tool)
			}
			return rt.CardClosed(c.member, card, id, outcome)
		})
		if err != nil && !errors.Is(err, ErrTalkootClosed) {
			w.diagf("talkoot %s: could not record a card of member %s: %v", run.id, c.member, err)
		}
	})
}

// workerSignal is one signal from a worker's events: a tool call that failed,
// or with attempt set a provider retry.
type workerSignal struct {
	id      string
	tool    string
	attempt int
	why     string
}

// record writes the signal for member.
func (s workerSignal) record(rt *talkoot.Router, member string) error {
	if s.attempt > 0 {
		return rt.Retried(member, s.attempt, failureReason(s.why))
	}
	return rt.ToolFailed(member, s.id, s.tool, failureReason(s.why))
}

// workerCalls keeps the name of each tool call one worker process runs, and
// reads the signals from its events.
type workerCalls struct {
	mu    sync.Mutex
	names map[string]string
}

// signals returns the signals event e carries. A terva worker sends
// tool_result and retry events, as the rpc wire does. A claude worker sends
// its tool results inside user_message events, and the translator makes a
// retry event from its api_retry.
func (c *workerCalls) signals(e swarm.Event) []workerSignal {
	str := func(m map[string]any, k string) string { v, _ := m[k].(string); return v }
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.names == nil {
		c.names = map[string]string{}
	}
	take := func(id string) string {
		name := c.names[id]
		delete(c.names, id)
		return name
	}
	switch e.Type {
	case "tool_call":
		if id, name := str(e.Data, "id"), str(e.Data, "name"); id != "" && name != "" {
			c.names[id] = name
		}
	case "tool_result":
		id := str(e.Data, "id")
		name := take(id)
		if failed, _ := e.Data["is_error"].(bool); failed {
			return []workerSignal{{id: id, tool: name, why: blockText(e.Data["content"])}}
		}
	case "task_end":
		// A call whose result never came, as in an interrupted turn, ends
		// with its turn.
		clear(c.names)
	case "retry":
		r, _ := e.Data["retry"].(map[string]any)
		if n := eventInt(r["attempt"]); n >= 1 {
			return []workerSignal{{attempt: n, why: str(r, "error")}}
		}
	case "assistant_message", "user_message":
		msg, _ := e.Data["message"].(map[string]any)
		blocks, _ := msg["content"].([]any)
		var out []workerSignal
		for _, b := range blocks {
			m, _ := b.(map[string]any)
			switch str(m, "type") {
			case "tool_use":
				if id, name := str(m, "id"), str(m, "name"); id != "" && name != "" {
					c.names[id] = name
				}
			case "tool_result":
				// ⚠️ Only a claude block names its call with tool_use_id. A
				// terva result arrives as a tool_result event, above, and a
				// block of it in a message must not count twice.
				id := str(m, "tool_use_id")
				if id == "" {
					continue
				}
				name := take(id)
				if failed, _ := m["is_error"].(bool); failed {
					out = append(out, workerSignal{id: id, tool: name, why: blockText(m["content"])})
				}
			}
		}
		return out
	}
	return nil
}

// eventInt reads a whole number from an event's data. A decoded event holds a
// float64, and an event a translator built in memory holds an int.
func eventInt(v any) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	case int64:
		return int(n)
	}
	return 0
}

// blockText returns the text of a tool result's content: a string, or a list
// of blocks whose text blocks it joins.
func blockText(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	list, _ := v.([]any)
	var parts []string
	for _, b := range list {
		m, _ := b.(map[string]any)
		if t, _ := m["type"].(string); t == "text" {
			if s, _ := m["text"].(string); s != "" {
				parts = append(parts, s)
			}
		}
	}
	return strings.Join(parts, " ")
}
