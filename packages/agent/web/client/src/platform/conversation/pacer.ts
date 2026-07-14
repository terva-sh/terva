// The streaming pacer: a jitter buffer between the wire and the transcript.
//
// Providers chunk their text_delta events wildly differently. Measured on one
// prompt: openai-codex delivers ~5 runes every ~20ms, while Anthropic's OAuth
// path delivers ~56 runes every ~460ms — the same throughput, an order of
// magnitude coarser. Rendered as they land, the first reads as typing and the
// second as paragraphs slapping onto the screen half a second apart.
//
// The TUI has smoothed this for as long as it has had a typewriter (see
// modes/stream_state.go, paceRate). The panel never did: it appended each delta
// straight into the rendered text, so the provider's chunking WAS the animation.
// This is that pacer, with the same policy and the same constants:
//
//   - while text is still arriving, release len(buffer)/PACE_DRAIN_TICKS runes
//     per tick, so the buffer settles at (arrival rate x horizon) and the reveal
//     runs continuously at the arrival rate instead of bursting and starving;
//   - once the message is complete there is no jitter left to absorb, so the
//     tail drains at the plain typewriter rate.
//
// It buffers EVENTS, not just text, because a transcript is ordered: a tool call
// that arrives while the prose introducing it is still painting has to wait for
// it, or the block snaps in mid-paragraph. Events that are not part of the
// transcript at all (usage, turn boundaries, permission prompts) bypass the queue
// and are delivered the moment they land.
import type { WireEvent } from '../ctrlproto/types'

// PACE_INTERVAL_MS is the tick interval. 16ms is one frame at 60Hz — the pacer
// can't usefully release text faster than the browser paints it.
export const PACE_INTERVAL_MS = 16

// PACE_DRAIN_TICKS is the jitter buffer's horizon, in ticks: while deltas are
// still arriving the pacer spreads whatever is queued across this many. 25 x 16ms
// is ~400ms of buffered latency — enough to bridge the silences between a coarse
// provider's chunks, short enough that the text never feels detached from the
// model producing it.
export const PACE_DRAIN_TICKS = 25

// PACE_MAX_RATE caps the catch-up, in runes/tick. It binds only on a deep buffer
// (a whole reply in one delta, or a local model outrunning the steady state):
// brisk enough to track any real provider, bounded so a deep buffer still reveals
// rather than dumps.
export const PACE_MAX_RATE = 24

// PACE_FLUSH_RATE is the typewriter rate for a COMPLETE message's tail: ~375
// runes/s at a 16ms tick.
export const PACE_FLUSH_RATE = 6

// ORDERED names the events whose place in the transcript is defined relative to
// the streamed text around them, so they must wait behind whatever the pacer has
// not painted yet.
//
// `snapshot` is on this list and that is the subtle one: the authoritative
// snapshot lands at the END of every turn, and merging it swaps the optimistic
// streaming row for the finalized message — in full. Let it through early and it
// would dump the very text the pacer is still revealing, one turn at a time.
const ORDERED = new Set<string>([
  'text_delta',
  'assistant_message',
  'user_message',
  'tool_call',
  'tool_result',
  'error',
  'notice',
  'snapshot',
])

type Entry = { kind: 'text'; runes: string[] } | { kind: 'event'; ev: WireEvent }

export class StreamPacer {
  private queue: Entry[] = []

  // emit delivers an event onward, exactly as if it had just arrived on the wire.
  constructor(private readonly emit: (ev: WireEvent) => void) {}

  // push takes one event off the wire. Non-transcript events go straight through;
  // the rest join the queue, and text coalesces onto the entry already there.
  push(ev: WireEvent): void {
    if (!ORDERED.has(ev.type)) {
      this.emit(ev)
      return
    }
    if (ev.type === 'text_delta') {
      const delta = ev.delta ?? ''
      if (!delta) return
      const tail = this.queue[this.queue.length - 1]
      // Array.from splits by code point, so an astral character (an emoji) is
      // never torn in half by a tick boundary.
      const runes = Array.from(delta)
      if (tail?.kind === 'text') tail.runes.push(...runes)
      else this.queue.push({ kind: 'text', runes })
      return
    }
    this.queue.push({ kind: 'event', ev })
  }

  // tick releases one frame's worth of the queue. Returns true while there is
  // still something buffered, so a caller can stop its timer when idle.
  tick(): boolean {
    // A run of events at the head has nothing buffered ahead of it — deliver it.
    while (this.queue.length && this.queue[0].kind === 'event') {
      const head = this.queue.shift() as { kind: 'event'; ev: WireEvent }
      this.emit(head.ev)
    }
    const head = this.queue[0]
    if (!head || head.kind !== 'text') return this.queue.length > 0

    // Anything queued BEHIND this text means the message already ended: no more
    // deltas can join it, so there is no jitter to absorb and the tail can drain
    // at the typewriter rate.
    const complete = this.queue.length > 1
    const rate = complete
      ? PACE_FLUSH_RATE
      : Math.min(PACE_MAX_RATE, Math.ceil(head.runes.length / PACE_DRAIN_TICKS))

    const delta = head.runes.splice(0, Math.min(rate, head.runes.length)).join('')
    if (head.runes.length === 0) this.queue.shift()
    if (delta) this.emit({ type: 'text_delta', delta } as WireEvent)
    return this.queue.length > 0
  }

  // reset drops everything buffered. The queued text belongs to a conversation
  // the panel is no longer showing (a session switch, a reconnect), and replaying
  // it into the new one would splice one transcript into another.
  reset(): void {
    this.queue = []
  }

  get idle(): boolean {
    return this.queue.length === 0
  }
}
