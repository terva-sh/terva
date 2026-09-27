package workspace

import (
	"strings"
	"sync"

	"terva.sh/terva/packages/agent/talkoot"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/provider"
)

// talkootReads are the talkoot deliveries queued into one session that no
// turn has read yet, oldest first.
//
// 🔑 A delivery is read when its text enters the transcript as a user
// message. That is the drain at a running turn's safe boundary, or the start
// of the turn the text begins. Both come before the model sees the text, so
// the member works in the new chain from its first step on it.
//
// 🔑 The header names the envelope, so no two deliveries share a text, and a
// stale entry never matches a later delivery.
//
// ⚠️ A person who types a delivery's exact text into the session reads it for
// the member. The chain still has a human root, and that person could post to
// the member anyway. A text that a user_message guard rewrites is never read,
// and the member keeps its chain.
type talkootReads struct {
	// mu is a leaf lock. No code takes another lock while it holds mu, so
	// endTurn may take it under wsSession.mu.
	mu      sync.Mutex
	pending []talkootRead
	watch   sync.Once
}

type talkootRead struct {
	text string
	read func()
}

// queueTalkoot queues a delivery's text into the session, and calls read
// when a turn reads it. The entry is in place before the text is queued, so
// a turn that starts at once finds it.
func (s *wsSession) queueTalkoot(text string, read func()) {
	s.reads.watch.Do(func() { s.reads.attach(s.agent) })
	s.reads.mu.Lock()
	s.reads.pending = append(s.reads.pending, talkootRead{text: strings.TrimSpace(text), read: read})
	s.reads.mu.Unlock()
	s.queue(text)
}

// attach watches ag for the two ends of a delivery: its text enters the
// transcript, or a user_message guard refuses it. buildSession attaches
// before the first turn.
//
// ⚠️ An event observer added during a turn misses that turn's events, because
// the agent snapshots them once per prompt. Only a bare test session attaches
// late, on its first delivery.
func (r *talkootReads) attach(ag *core.Agent) {
	ag.AddMessageObserver(r.observe)
	ag.AddEventObserver(r.onEvent)
}

// onEvent forgets a delivery that a user_message guard refused. The event
// carries the text as it was queued.
func (r *talkootReads) onEvent(ev core.AgentEvent) {
	if rej, ok := ev.(core.EvUserMessageRejected); ok {
		r.forget([]string{rej.Text})
	}
}

// observe reads each delivery whose text a user message carries. It runs on
// the turn's goroutine before the next model call, with no lock held.
func (r *talkootReads) observe(m provider.Message) {
	if m.Role != provider.RoleUser || m.Meta[core.MetaSynthetic] == "true" {
		return
	}
	var reads []func()
	r.mu.Lock()
	for _, b := range m.Content {
		if len(r.pending) == 0 {
			break
		}
		if tb, ok := b.(provider.TextBlock); ok {
			if fn := r.takeLocked(strings.TrimSpace(tb.Text)); fn != nil {
				reads = append(reads, fn)
			}
		}
	}
	r.mu.Unlock()
	for _, fn := range reads {
		fn()
	}
}

// forget drops the deliveries whose texts left the queue unread and are known
// exactly: a guard's refusal. An entry otherwise stays until its text arrives.
// A text that a guard rewrites, or that a person edits out of the queue, never
// arrives, and its entry matches nothing until the session closes, because
// each text names its envelope.
//
// 🚨 No cap evicts an entry. An evicted entry whose text is still queued would
// be read with no receipt, and the member would stay in its old chain.
func (r *talkootReads) forget(texts []string) {
	if len(texts) == 0 {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, t := range texts {
		r.takeLocked(strings.TrimSpace(t))
	}
}

// split divides the texts a failed turn drained from its queue into the
// deliveries that wait for a read, which stay queued, and the rest, which
// the turn drops.
func (r *talkootReads) split(texts []string) (kept, dropped []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, t := range texts {
		if r.pendingLocked(strings.TrimSpace(t)) {
			kept = append(kept, t)
		} else {
			dropped = append(dropped, t)
		}
	}
	return kept, dropped
}

func (r *talkootReads) pendingLocked(text string) bool {
	for _, p := range r.pending {
		if p.text == text {
			return true
		}
	}
	return false
}

func (r *talkootReads) takeLocked(text string) func() {
	for i, p := range r.pending {
		if p.text == text {
			r.pending = append(r.pending[:i], r.pending[i+1:]...)
			return p.read
		}
	}
	return nil
}

// talkootRead moves a member into the chain of a delivery its session's turn
// has read.
//
// 🚨 The session must still hold the member's seat in run. A session that
// lost the seat reads the text as a stranger, and the member's new session
// does not work in that chain.
func (w *Workspace) talkootRead(sessID string, run *talkootRun, member string, r talkoot.Receipt) {
	w.talkoot.mu.Lock()
	b := w.talkoot.seats[sessID]
	w.talkoot.mu.Unlock()
	if b == nil || b.run != run || b.member != member {
		return
	}
	if err := (talkootSeat{b: b, w: w}).read(r); err != nil {
		w.diagf("talkoot: session %s could not report a read: %v", sessID, err)
	}
}
