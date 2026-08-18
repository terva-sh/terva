import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import { describe, expect, it } from 'vitest'

import { StreamPacer } from './pacer'
import { TRANSCRIPT_EVENTS } from './store'

// Two answers to "which events become transcript rows" lived in two files, and
// they had already diverged by three.
//
// applyEvent appends rows for stall, escalation and user_message_rejected. None
// of the three was in the pacer's ORDERED set, so StreamPacer.push emitted them
// IMMEDIATELY while ~400ms of text sat in the jitter buffer. Core emits a stall
// right after a tool result, so during any stuck-loop nudge the hatch row landed
// while the streaming assistant item was still last — and the next released
// text_delta, seeing a non-streaming last item, opened a SECOND assistant
// bubble. The reply split in two around a note sitting above the prose it
// described, and the first half kept its spinner until the end-of-turn snapshot.
// pacer.test.ts pinned ordering for tool_call and snapshot only, so nothing
// failed.
//
// ORDERED is now derived from TRANSCRIPT_EVENTS. This reads the case labels back
// out of the SOURCE rather than restating them, because a third hand-written
// list would drift exactly as the second one did.

function applyEventCaseLabels(): string[] {
  const src = readFileSync(join(__dirname, 'store.ts'), 'utf8')
  // Isolate applyEvent's switch: other switches in this file (itemsFromMessages,
  // the notice renderer) would otherwise inflate the answer and mask a real gap.
  const start = src.indexOf('export function applyEvent(')
  expect(start, 'applyEvent not found in store.ts — this census is anchored on it').toBeGreaterThan(-1)
  const body = src.slice(start)
  const end = body.indexOf('    default:\n      return items\n')
  expect(end, 'applyEvent default arm not found — the anchor moved, fix this census').toBeGreaterThan(-1)
  const labels = new Set<string>()
  for (const m of body.slice(0, end).matchAll(/case '([a-z_]+)':/g)) labels.add(m[1])
  return [...labels].sort()
}

describe('transcript event census', () => {
  it('TRANSCRIPT_EVENTS names exactly the events applyEvent turns into rows', () => {
    const labels = applyEventCaseLabels()
    expect(labels.length, 'the switch scan found nothing; the anchors moved and this census proves nothing').toBeGreaterThan(5)
    expect(labels).toEqual([...TRANSCRIPT_EVENTS].sort())
  })

  it('the pacer holds back every transcript event, plus the snapshot', () => {
    // Driven through the real StreamPacer rather than by reading its source, so
    // a pacer that stopped deriving its set and went back to a literal is caught
    // by BEHAVIOUR.
    for (const type of applyEventCaseLabels()) {
      const emitted: string[] = []
      const pacer = new StreamPacer((ev) => emitted.push(ev.type))
      // A text_delta first, so the queue is non-empty and anything that jumps it
      // is observable: an ordered event must NOT appear before the text drains.
      pacer.push({ type: 'text_delta', delta: 'hello there' } as never)
      pacer.push({ type } as never)
      expect(emitted, `${type} bypassed the pacer's queue; it becomes a transcript row, so it has a place relative to the text around it`).toEqual([])
    }
  })

  it('a non-transcript event still bypasses the queue', () => {
    // The complement: a pacer that held EVERYTHING back would satisfy the test
    // above while stalling usage, turn boundaries and permission prompts behind
    // the jitter buffer.
    const emitted: string[] = []
    const pacer = new StreamPacer((ev) => emitted.push(ev.type))
    pacer.push({ type: 'text_delta', delta: 'hello there' } as never)
    pacer.push({ type: 'usage' } as never)
    expect(emitted).toEqual(['usage'])
  })
})
