package modes

import "strings"

// streamState is the typewriter pipeline's state machine (TUI plan
// Phase 1.3). It owns what used to be four loose coordinated fields
// on Interactive (streamOn, streaming, streamPending,
// streamFlushPending) plus the per-tool reveal gates, so the legal
// transitions live in one place instead of being re-derived at every
// event-handler and pacer site.
//
//	idle ──beginTurn/beginAssistant/appendDelta──► live
//	live ──finishMessage with runes still queued──► flushing
//	flushing ──pacer drains the last rune──► idle
//	any  ──reset (abort, compact, turn cleanup)──► idle
//
// streamState does no locking: every method must be called with the
// owning Interactive's mu held, exactly like the fields it replaced.
type streamState struct {
	phase streamPhase

	// painted is the streamed assistant text already visible on
	// screen; pending holds the runes buffered behind it. The pacer
	// moves runes from pending to painted a few per tick so the
	// typewriter feel is identical regardless of how fat the
	// provider's delta chunks are.
	painted strings.Builder
	pending []rune

	// gates records, per tool-call id, the painted-rune count the
	// pacer must reach before that tool block may render. Gating on
	// "everything queued when the tool arrived" guarantees the prose
	// introducing a tool finishes typing out above it instead of the
	// block snapping in mid-paragraph. Gate 0 means "show now".
	gates map[string]int
}

type streamPhase int

const (
	// streamIdle: no assistant text is streaming.
	streamIdle streamPhase = iota
	// streamLive: deltas are arriving and/or the pacer is painting.
	streamLive
	// streamFlushing: the final message has landed
	// (EvAssistantMessage) but the pacer is still draining queued
	// runes. The finalised transcript message stays hidden until the
	// drain completes so the text doesn't appear twice.
	streamFlushing
)

func newStreamState() *streamState {
	return &streamState{gates: map[string]int{}}
}

// active reports whether a streaming block should render at all
// (live or flushing — the old streamOn flag).
func (s *streamState) active() bool { return s.phase != streamIdle }

// flushing reports the drain-after-final-message window, during which
// buildChat hides the already-complete transcript message.
func (s *streamState) flushing() bool { return s.phase == streamFlushing }

// visible returns the painted text; visibleLen its rune length.
func (s *streamState) visible() string { return s.painted.String() }

func (s *streamState) visibleLen() int { return s.painted.Len() }

// beginTurn arms streaming for a fresh user-initiated turn. Pending
// runes are deliberately left alone: abort paths call reset(), so
// anything still queued here belongs to this prompt's stream.
func (s *streamState) beginTurn() {
	s.painted.Reset()
	s.phase = streamLive
}

// beginAssistant fires at the top of every assistant message,
// including follow-up turns after tool use: drop any leftovers so the
// new message typewriter-streams from its first rune instead of
// popping in after stale state.
func (s *streamState) beginAssistant() {
	s.painted.Reset()
	s.pending = s.pending[:0]
	s.phase = streamLive
}

// appendDelta queues streamed text behind the pacer.
func (s *streamState) appendDelta(delta string) {
	s.pending = append(s.pending, []rune(delta)...)
	if s.phase == streamIdle {
		s.phase = streamLive
	}
}

// finishMessage handles EvAssistantMessage. When the pacer still has
// runes to paint it latches the flushing phase and reports
// deferred=true: the caller must leave state alone and let the pacer
// finish the reveal. Otherwise streaming state resets now.
func (s *streamState) finishMessage() (deferred bool) {
	if len(s.pending) > 0 {
		s.phase = streamFlushing
		return true
	}
	s.reset()
	return false
}

// promptReturned handles agent.Prompt coming back: if the pacer has
// nothing left to paint the stream is over; if it does, leave the
// machine alone — the pacer keeps painting after the turn ends and
// retires the state itself.
func (s *streamState) promptReturned() {
	if len(s.pending) == 0 {
		s.reset()
	}
}

// paceRate returns how many runes the pacer should release this tick.
//
// A FIXED rate cannot smooth a provider that arrives in fat, infrequent
// chunks. Measured on the same prompt: openai-codex delivers ~5 runes every
// ~20ms, while Anthropic's OAuth path delivers ~56 runes every ~460ms — the
// same throughput, an order of magnitude coarser. Draining that at a fixed 6
// runes/tick empties one chunk in ~310ms and then paints NOTHING for the
// remaining ~150ms, so the reply lands in visible lumps. The pacer was meant
// to normalize exactly this, and a fixed rate cannot: it drains as fast as it
// can and then starves.
//
// So the rate follows the buffer — spread whatever is queued across
// paceDrainTicks. The buffer settles at (arrival rate x horizon) runes and the
// pacer paints continuously at the arrival rate, which is what a jitter buffer
// is: a bounded, ~400ms latency traded for an even reveal. The ceiling keeps it
// above zero while anything is queued, so the typewriter can never stall
// mid-word; paceMaxRate bounds the catch-up on a deep buffer.
//
// Flushing is the exception. The final message has landed and no more deltas
// are coming, so there is no jitter left to absorb — drain the tail at the
// plain typewriter rate instead of trickling it out at the arrival rate.
func (s *streamState) paceRate() int {
	n := len(s.pending)
	if n == 0 {
		return 0
	}
	if s.phase == streamFlushing {
		return paintPaceRate
	}
	rate := (n + paceDrainTicks - 1) / paceDrainTicks // ceil: never 0 while queued
	if rate > paceMaxRate {
		rate = paceMaxRate
	}
	return rate
}

// paceTick advances the typewriter by up to n runes. painted=true
// means new text is visible; finished=true means a deferred
// finishMessage just completed and the transcript may reveal the
// finalised message. Either implies the caller should repaint.
func (s *streamState) paceTick(n int) (painted, finished bool) {
	if len(s.pending) == 0 {
		if s.phase == streamFlushing {
			s.reset()
			return false, true
		}
		return false, false
	}
	if n > len(s.pending) {
		n = len(s.pending)
	}
	s.painted.WriteString(string(s.pending[:n]))
	s.pending = s.pending[n:]
	return true, false
}

// reset clears every piece of streaming state in one shot (abort,
// compact finish, queue drain) so the pacer can't drain stale runes
// from a prior turn. Gates open rather than clear: the comparison
// against a freshly-reset painted buffer would otherwise re-hide
// tools that had already cleared their gate.
func (s *streamState) reset() {
	s.painted.Reset()
	s.pending = s.pending[:0]
	s.phase = streamIdle
	s.openAllGates()
}

// gateTool records the reveal position for a tool call: everything
// already painted plus everything queued when it arrived. Only gates
// while text is actively streaming — for tool-only turns and replayed
// sessions the tool shows immediately. First registration wins so a
// later EvToolCall can't move an existing gate.
func (s *streamState) gateTool(id string) {
	if _, ok := s.gates[id]; ok {
		return
	}
	if !s.active() {
		s.gates[id] = 0
		return
	}
	s.gates[id] = s.painted.Len() + len(s.pending)
}

// gateOpen reports whether the pacer has painted past the position
// recorded when the tool call arrived.
func (s *streamState) gateOpen(id string) bool {
	gate, ok := s.gates[id]
	if !ok || gate == 0 {
		return true
	}
	return s.painted.Len() >= gate
}

// resetGates drops all gate registrations (new turn, new session,
// post-compact cleanup — alongside the toolCalls/toolOrder resets).
func (s *streamState) resetGates() {
	s.gates = map[string]int{}
}

// openAllGates marks every registered tool as revealable without
// dropping the registrations themselves.
func (s *streamState) openAllGates() {
	for id := range s.gates {
		s.gates[id] = 0
	}
}
