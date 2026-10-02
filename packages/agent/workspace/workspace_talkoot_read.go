package workspace

import (
	"strings"
	"sync"
	"sync/atomic"

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
	// taken counts the deliveries any turn of this session has read.
	taken atomic.Uint64
	// fresh is set when a turn starts and cleared by its first user message.
	// opened counts the turns whose first user message was a person's
	// delivery. A turn that leaves opened unchanged was started by a person
	// typing into the session, or by a teammate's handoff, so its final reply
	// is not mirrored to the room (launchTurn). A handoff's answer belongs to
	// the teammate, who reports onward.
	fresh  atomic.Bool
	opened atomic.Uint64
	// users counts the user messages a person or a delivery wrote. A turn that
	// adds more than one took input after its opening delivery: another
	// delivery moves the member's chain, and a person's message is private.
	// Either way the final reply no longer answers only the opening delivery,
	// so launchTurn does not mirror it.
	users atomic.Uint64
	// replies counts the assistant messages this session's turns appended. A
	// turn that leaves it unchanged produced no reply of its own, so the last
	// assistant message in the transcript belongs to an earlier turn and must
	// not be published again (launchTurn).
	replies atomic.Uint64
}

type talkootRead struct {
	text string
	// person is set when a person sent the delivery, not a member.
	person bool
	read   func()
}

// queueTalkoot queues a delivery's text into the session, and calls read
// when a turn reads it. The entry is in place before the text is queued, so
// a turn that starts at once finds it.
func (s *wsSession) queueTalkoot(text string, person bool, read func()) {
	s.reads.watch.Do(func() { s.reads.attach(s.agent) })
	s.reads.mu.Lock()
	s.reads.pending = append(s.reads.pending, talkootRead{text: strings.TrimSpace(text), person: person, read: read})
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
	if m.Role == provider.RoleAssistant {
		r.replies.Add(1)
		return
	}
	if !core.IsUserTurn(m) {
		return
	}
	r.users.Add(1)
	var reads []talkootRead
	r.mu.Lock()
	for _, b := range m.Content {
		if len(r.pending) == 0 {
			break
		}
		if tb, ok := b.(provider.TextBlock); ok {
			if d, ok := r.takeLocked(strings.TrimSpace(tb.Text)); ok {
				reads = append(reads, d)
			}
		}
	}
	r.mu.Unlock()
	// 🚨 An opening message that carries two deliveries answers two chains,
	// and the member is active in only the last one. Such a turn is not
	// mirrored. Core appends one text per message today, so this is a guard.
	// A teammate's handoff opens a turn that reports to the teammate, so only
	// a person's delivery counts.
	if r.fresh.Swap(false) && len(reads) == 1 && reads[0].person {
		r.opened.Add(1)
	}
	if len(reads) > 0 {
		r.taken.Add(uint64(len(reads)))
	}
	for _, d := range reads {
		d.read()
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

func (r *talkootReads) takeLocked(text string) (talkootRead, bool) {
	for i, p := range r.pending {
		if p.text == text {
			r.pending = append(r.pending[:i], r.pending[i+1:]...)
			return p, true
		}
	}
	return talkootRead{}, false
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
