package modes

import (
	"context"
	"sync"
	"time"
)

// turnEngine owns the turn lifecycle state (TUI plan Phase 2b): the
// busy flag, the active turn's cancel func, the
// streaming pipeline, and the pacer — everything that decides whether
// a prompt runs now or queues — behind one mutex.
//
// Why one lock: the old layout guarded this state with the TUI's big
// i.mu, but producers (key loop, chat bridge, swarm watchers,
// extension prompts) released that lock between *checking* busy and
// *acting* on the answer. A prompt queued in the gap between the
// agent loop's final queue check and busy flipping false was stranded
// — it sat in the queue until some unrelated later turn drained it.
// Here the check and the action are one critical section: release
// flips busy off and shifts the next queued prompt under the same
// hold, so there is no instant where a message can fall between the
// two.
//
// The claim half of that pairing (claimOrQueue: claim, or queue under
// the same hold) is gone. On the carrier path the daemon's wsSession
// is the busy arbiter, not this engine — a producer that loses either
// fails claimCarrier or gets CodeBusy back from Prompt, and queues
// through the service so every client's queued view converges.
//
// Queueing is unified on the agent's own queue (Phase 2d): a running
// loop drains it at safe boundaries (answered within the same turn),
// and release shifts its head to restart when the loop has already
// exited. The old host-side i.queued shadow queue is gone.
//
// Lock discipline: engine.mu is a LEAF lock. Engine methods may take
// agent-internal mutexes (agent code never calls back into the
// engine), but must never invoke TUI callbacks or take i.mu while
// holding engine.mu. The TUI may call engine methods while holding
// i.mu (i.mu → engine.mu is the one allowed nesting).
type turnEngine struct {
	mu             sync.Mutex
	busy           bool
	autoCompacting bool
	cancel         context.CancelFunc
	stream         *streamState
}

func newTurnEngine() *turnEngine {
	return &turnEngine{stream: newStreamState()}
}

// The engine once held the live *core.Agent (SetAgent / Agent), the TUI's
// window into the daemon's session for rendering and management dialogs. Every
// reader migrated to a snapshot or a surface (plan 4.1); /btw, the last, moved
// to the sidechat surface. The engine now tracks only the local UI slot —
// busy, the active cancel, the stream — and reaches for no agent at all.

// ---- slot transitions ----

// Busy reports whether a turn (or compaction) is in flight.
func (t *turnEngine) Busy() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.busy
}

// AutoCompacting reports whether the in-flight work is a
// system-initiated compaction.
func (t *turnEngine) AutoCompacting() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.autoCompacting
}

// claimCompact claims the slot for a compaction run (no stream
// arming — the summary must not paint into the chat). Reports false
// when a turn is already in flight.
func (t *turnEngine) claimCompact(cancel context.CancelFunc, auto bool) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.busy {
		return false
	}
	t.busy = true
	t.cancel = cancel
	t.autoCompacting = auto
	return true
}

// claimSlot claims the busy slot for non-turn work that must still
// block prompts and be esc-cancellable (the ! shell escape). No
// stream arming, no auto-compact flag.
func (t *turnEngine) claimSlot(cancel context.CancelFunc) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.busy {
		return false
	}
	t.busy = true
	t.cancel = cancel
	return true
}

// releaseSlot ends non-turn in-flight work (the ! shell escape): flips busy
// off, retires the stream if it has fully drained (the pacer keeps painting any
// leftovers), and touches NOTHING else.
//
// It used to own a queue decision — drain on cancel, shift-and-restart on
// success — inherited from the legacy path, where the engine's agent was the
// TUI's own. It no longer is: `turns.agent` was the DAEMON's session agent, so
// those legacy semantics reached across into shared state. Cancelling a shell
// escape drained the whole queue, discarding prompts another device or a paired
// chat DM had queued, and with no queue_updated broadcast every other client
// went on rendering chips for messages that no longer existed. Finishing one
// shifted the head and re-dispatched it locally, racing the daemon's own
// end-of-turn shift for the same message.
//
// The Workspace owns post-turn queue policy in carrier mode — releaseCarrier
// has said so all along — and it needs no help here. A prompt typed during a
// shell escape reaches the daemon through Queue: idle, it starts immediately;
// busy, it waits on the agent's queue and the daemon's endTurn shifts it. The
// escape's slot is a LOCAL UI reflection (spinner, input gate, esc target), and
// releasing it is all this has to do.
func (t *turnEngine) releaseSlot() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.busy = false
	t.autoCompacting = false
	t.cancel = nil
	t.stream.promptReturned()
}

// ---- carrier slot transitions (the default ctrlproto TUI path) ----
//
// In carrier mode the wsSession is the busy ARBITER (its Prompt returns
// CodeBusy) and the Workspace owns post-turn queue policy; the engine's slot
// is the local UI reflection of that state — spinner, input gating, stream
// arming — driven by the event stream instead of a synchronous Prompt return.

// claimCarrier claims the turn slot for a carrier-dispatched turn: it arms the
// stream, but has no queue fallback — carrier mode queues through the
// WorkspaceService so every client's queued view converges — and cancel routes
// the esc/ctrl+c plumbing to the service's Cancel instead of a local turn
// context.
func (t *turnEngine) claimCarrier(cancel context.CancelFunc) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.busy {
		return false
	}
	t.busy = true
	t.cancel = cancel
	t.stream.beginTurn()
	t.stream.resetGates()
	return true
}

// reclaimCarrier re-arms the busy slot for a turn observed on the stream that
// this client didn't dispatch (a daemon queue restart, another device's
// prompt). Busy + cancel only — the turn's own assistant_start arms the
// stream. Reports whether it claimed; false when the slot is already held
// (our own dispatch claimed it, or turn_start fired for a later step).
func (t *turnEngine) reclaimCarrier(cancel context.CancelFunc) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.busy {
		return false
	}
	t.busy = true
	t.cancel = cancel
	return true
}

// releaseCarrier ends carrier-mode in-flight work on the stream's definitive
// "done": flips busy off and retires the drained stream. No queue shift — the
// Workspace owns post-turn queue restart in carrier mode. Reports whether the
// slot was actually held, so a duplicate "done" (the cancel-during-tools path
// produces two) is a no-op.
func (t *turnEngine) releaseCarrier() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.busy {
		return false
	}
	t.busy = false
	t.autoCompacting = false
	t.cancel = nil
	t.stream.promptReturned()
	return true
}

// markCompacting flags carrier-mode in-flight work as a policy compaction
// (wire compact_start/compact_end) so the status bar shows the auto note.
func (t *turnEngine) markCompacting(on bool) {
	t.mu.Lock()
	t.autoCompacting = on
	t.mu.Unlock()
}

// cancelActive cancels the in-flight turn's context, if any.
// The cancel func runs outside the lock.
func (t *turnEngine) cancelActive() bool {
	t.mu.Lock()
	cancel := t.cancel
	t.mu.Unlock()
	if cancel == nil {
		return false
	}
	cancel()
	return true
}

// ---- streaming (event side; called from the agent-event sink) ----

// BeginAssistant arms the typewriter for a fresh assistant message
// (every oneTurn start, including follow-ups after tool use) and
// drops stale tool gates.
func (t *turnEngine) BeginAssistant() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.stream.beginAssistant()
	t.stream.resetGates()
}

// AppendDelta queues streamed text behind the pacer.
func (t *turnEngine) AppendDelta(delta string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.stream.appendDelta(delta)
}

// FinishMessage handles the final assistant message; deferred=true
// means the pacer still has runes to paint and owns the reveal.
func (t *turnEngine) FinishMessage() (deferred bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.stream.finishMessage()
}

// GateTool registers a tool block's reveal position.
func (t *turnEngine) GateTool(id string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.stream.gateTool(id)
}

// ResetGates drops all gate registrations (new turn, /clear, /cd,
// post-compact cleanup).
func (t *turnEngine) ResetGates() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.stream.resetGates()
}

// ResetStream clears all streaming state (aborts).
func (t *turnEngine) ResetStream() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.stream.reset()
}

// ---- streaming (render side) ----

// turnRenderState is the render pass's one-lock snapshot of the
// engine: everything buildChat and the status bar need for a frame.
type turnRenderState struct {
	busy           bool
	autoCompacting bool
	streamVisible  string
	streamLen      int
	streamActive   bool
	streamFlushing bool
}

func (t *turnEngine) renderState() turnRenderState {
	t.mu.Lock()
	defer t.mu.Unlock()
	return turnRenderState{
		busy:           t.busy,
		autoCompacting: t.autoCompacting,
		streamVisible:  t.stream.visible(),
		streamLen:      t.stream.visibleLen(),
		streamActive:   t.stream.active(),
		streamFlushing: t.stream.flushing(),
	}
}

// GateOpen reports whether a gated tool block may render yet.
func (t *turnEngine) GateOpen(id string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.stream.gateOpen(id)
}

// ---- pacer ----

// runPacer advances the typewriter a small batch per tick, calling
// invalidate (outside the lock) whenever the visible text changed or
// a deferred finish completed. Stops when ctx cancels.
//
// Why a pacer: providers differ wildly in how they chunk their
// text_delta events. The API-key path on Anthropic emits ~30 drips
// for a 400-token summary; the OAuth path can coalesce the same
// summary into 3 fat chunks, visually indistinguishable from "the
// whole reply just appeared". The pacer normalizes that so every
// path looks the same on screen.
func (t *turnEngine) runPacer(ctx context.Context, invalidate func()) {
	tick := time.NewTicker(paintPaceInterval)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			t.mu.Lock()
			painted, finished := t.stream.paceTick(paintPaceRate)
			t.mu.Unlock()
			if painted || finished {
				invalidate()
			}
		}
	}
}
