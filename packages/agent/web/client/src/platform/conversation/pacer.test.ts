import { describe, it, expect } from 'vitest'
import { StreamPacer, PACE_DRAIN_TICKS, PACE_MAX_RATE } from './pacer'
import type { WireEvent } from '../ctrlproto/types'

// A recording pacer: everything it emits, in order.
function harness() {
  const out: WireEvent[] = []
  const pacer = new StreamPacer((ev) => out.push(ev))
  const text = () =>
    out
      .filter((e) => e.type === 'text_delta')
      .map((e) => e.delta ?? '')
      .join('')
  // drain runs the pacer to completion and reports how many runes each tick
  // revealed — the panel's equivalent of a frame-by-frame recording.
  const drain = (maxTicks = 2000) => {
    const painted: number[] = []
    for (let i = 0; i < maxTicks && !pacer.idle; i++) {
      const before = text().length
      pacer.tick()
      painted.push(text().length - before)
    }
    return painted
  }
  return { pacer, out, text, drain }
}

const delta = (s: string): WireEvent => ({ type: 'text_delta', delta: s }) as WireEvent

describe('StreamPacer', () => {
  // The measured shape of a Claude Sonnet 4.5 reply over the subscription OAuth
  // path: 225 runes in FOUR deltas about 460ms (~29 ticks) apart. Rendered raw —
  // which is what the panel used to do — that is a 117-character paragraph
  // appearing at once, then nothing at all for half a second.
  it('bridges the silences between a coarse provider’s chunks', () => {
    const { pacer, drain, text } = harness()
    const arrivals = [
      { tick: 0, runes: 3 },
      { tick: 29, runes: 117 },
      { tick: 58, runes: 56 },
      { tick: 87, runes: 49 },
    ]

    const painted: number[] = []
    let produced = 0
    for (let tick = 0; tick <= 87; tick++) {
      const a = arrivals.find((x) => x.tick === tick)
      if (a) {
        pacer.push(delta('x'.repeat(a.runes)))
        produced += a.runes
      }
      const before = text().length
      pacer.tick()
      painted.push(text().length - before)
    }

    // From the first real chunk onward the reveal must be continuous. The pacer
    // cannot bridge the FIRST gap — 3 runes arrive and then the provider is
    // silent; there is nothing buffered to spend.
    const stalls = painted.slice(29).filter((n) => n === 0).length
    expect(stalls).toBe(0)

    drain()
    expect(text()).toHaveLength(produced)
  })

  it('never dumps: no tick reveals more than the catch-up cap', () => {
    const { pacer, drain } = harness()
    pacer.push(delta('x'.repeat(2000))) // a whole reply in one delta
    for (const n of drain()) expect(n).toBeLessThanOrEqual(PACE_MAX_RATE)
  })

  it('spreads a buffer across the drain horizon rather than a fixed rate', () => {
    const { pacer, out } = harness()
    pacer.push(delta('x'.repeat(PACE_DRAIN_TICKS * 4))) // 100 runes buffered
    pacer.tick()
    // 100 / 25 = 4 runes this tick: the rate follows the buffer. A fixed rate
    // would release the same handful regardless of how far behind it is.
    expect((out[0].delta ?? '').length).toBe(4)
  })

  // Ordering is the reason the pacer buffers events and not just text: a tool
  // block that snaps in while the prose introducing it is still typing reads as
  // a glitch.
  it('holds a transcript event behind text that has not finished painting', () => {
    const { pacer, out } = harness()
    pacer.push(delta('x'.repeat(200)))
    pacer.push({ type: 'tool_call', id: 't1', name: 'read' } as WireEvent)

    pacer.tick()
    expect(out.some((e) => e.type === 'tool_call')).toBe(false)

    for (let i = 0; i < 200 && !pacer.idle; i++) pacer.tick()
    expect(out[out.length - 1].type).toBe('tool_call')
  })

  // The snapshot is authoritative and carries the finalized message IN FULL. It
  // lands at the end of every turn, so letting it through early would dump the
  // very text the pacer is still revealing.
  it('holds the end-of-turn snapshot behind the text it would otherwise dump', () => {
    const { pacer, out } = harness()
    pacer.push(delta('x'.repeat(200)))
    pacer.push({ type: 'assistant_message' } as WireEvent)
    pacer.push({ type: 'snapshot' } as WireEvent)

    pacer.tick()
    expect(out.some((e) => e.type === 'snapshot')).toBe(false)

    for (let i = 0; i < 400 && !pacer.idle; i++) pacer.tick()
    expect(out.map((e) => e.type).filter((t) => t !== 'text_delta')).toEqual([
      'assistant_message',
      'snapshot',
    ])
  })

  // Anything that is not part of the transcript is about the session, not the
  // reply — a permission prompt must not wait on a paragraph.
  it('lets non-transcript events through immediately', () => {
    const { pacer, out } = harness()
    pacer.push(delta('x'.repeat(200)))
    pacer.push({ type: 'permission_request' } as WireEvent)
    pacer.push({ type: 'usage' } as WireEvent)
    pacer.push({ type: 'turn_end' } as WireEvent)
    expect(out.map((e) => e.type)).toEqual(['permission_request', 'usage', 'turn_end'])
  })

  it('drops what it holds on a session switch', () => {
    const { pacer, out, text } = harness()
    pacer.push(delta('x'.repeat(200)))
    pacer.reset()
    pacer.tick()
    expect(pacer.idle).toBe(true)
    expect(text()).toBe('')
    expect(out).toHaveLength(0)
  })

  it('never tears an astral character across a tick', () => {
    const { pacer, drain, text } = harness()
    const emoji = '🌊'
    pacer.push(delta(emoji.repeat(60)))
    drain()
    expect(text()).toBe(emoji.repeat(60))
    expect(text()).not.toContain('�')
  })
})
