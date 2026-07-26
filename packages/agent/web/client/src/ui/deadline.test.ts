import { describe, expect, it } from 'vitest'

import {
  DEADLINE_RAMP_MS,
  DEADLINE_SOON_MS,
  DEADLINE_URGENT_MS,
  deadlineClass,
  deadlineOf,
  deadlineStyle,
} from './deadline'

const NOW = Date.parse('2026-08-01T12:00:00Z')
const inMs = (ms: number) => new Date(NOW + ms).toISOString()
const HOUR = 60 * 60 * 1000
const DAY = 24 * HOUR

describe('deadlineOf', () => {
  it('names each band by how much time is left', () => {
    expect(deadlineOf(inMs(6 * DAY), NOW).level).toBe('calm')
    expect(deadlineOf(inMs(3 * DAY), NOW).level).toBe('near')
    expect(deadlineOf(inMs(12 * HOUR), NOW).level).toBe('soon')
    expect(deadlineOf(inMs(2 * HOUR), NOW).level).toBe('urgent')
  })

  // The boundaries are the whole contract — "within 24 hours" has to include
  // the instant 24 hours out, or the band opens a millisecond late.
  it('is inclusive at every threshold', () => {
    expect(deadlineOf(inMs(DEADLINE_URGENT_MS), NOW).level).toBe('urgent')
    expect(deadlineOf(inMs(DEADLINE_URGENT_MS + 1), NOW).level).toBe('soon')
    expect(deadlineOf(inMs(DEADLINE_SOON_MS), NOW).level).toBe('soon')
    expect(deadlineOf(inMs(DEADLINE_SOON_MS + 1), NOW).level).toBe('near')
    expect(deadlineOf(inMs(DEADLINE_RAMP_MS), NOW).level).toBe('near')
    expect(deadlineOf(inMs(DEADLINE_RAMP_MS + 1), NOW).level).toBe('calm')
  })

  // A lapsed credit is over, not maximally urgent. Ringing it red would put the
  // loudest row in the list on the one thing nothing can be done about.
  it('treats a lapsed deadline as expired, not urgent', () => {
    const d = deadlineOf(inMs(-HOUR), NOW)
    expect(d.level).toBe('expired')
    expect(d.msLeft).toBeLessThan(0)
    expect(deadlineOf(inMs(0), NOW).level).toBe('expired')
  })

  it('reports nothing to colour for a missing or unparseable expiry', () => {
    expect(deadlineOf(undefined, NOW).level).toBe('none')
    expect(deadlineOf('', NOW).level).toBe('none')
    expect(deadlineOf('soon-ish', NOW).level).toBe('none')
  })

  it('ramps from 0 at the far end to 1 at the moment it lapses', () => {
    expect(deadlineOf(inMs(DEADLINE_RAMP_MS), NOW).ratio).toBeCloseTo(0, 5)
    expect(deadlineOf(inMs(DEADLINE_RAMP_MS / 2), NOW).ratio).toBeCloseTo(0.5, 5)
    expect(deadlineOf(inMs(HOUR), NOW).ratio).toBeGreaterThan(0.99)
  })

  // Outside the ramp the ratio must not run negative, or the colour mix walks
  // back past --ui-warn into an undefined blend.
  it('clamps the ratio outside the ramp', () => {
    expect(deadlineOf(inMs(30 * DAY), NOW).ratio).toBe(0)
    expect(deadlineOf(inMs(-30 * DAY), NOW).ratio).toBe(1)
  })
})

describe('deadlineClass / deadlineStyle', () => {
  it('marks only the bands that should look different', () => {
    expect(deadlineClass(deadlineOf(inMs(12 * HOUR), NOW))).toBe('ui-deadline ui-deadline--soon')
    expect(deadlineClass(deadlineOf(inMs(2 * HOUR), NOW))).toBe('ui-deadline ui-deadline--urgent')
    expect(deadlineClass(deadlineOf(inMs(3 * DAY), NOW))).toBe('ui-deadline ui-deadline--near')
  })

  // Calm, absent and expired rows must be indistinguishable from any other row.
  it('leaves an ordinary row unmarked', () => {
    expect(deadlineClass(deadlineOf(inMs(6 * DAY), NOW))).toBe('')
    expect(deadlineClass(deadlineOf(undefined, NOW))).toBe('')
    expect(deadlineClass(deadlineOf(inMs(-HOUR), NOW))).toBe('')
  })

  it('hands the ramp position to CSS only where something is coloured', () => {
    expect(deadlineStyle(deadlineOf(inMs(DEADLINE_RAMP_MS / 2), NOW))).toEqual({ '--ui-urgency': '0.50' })
    // 2h of a 5d ramp — nearly, but never quite, the far end: 1.00 belongs to
    // the lapsed case alone, which carries no style at all.
    expect(deadlineStyle(deadlineOf(inMs(2 * HOUR), NOW))).toEqual({ '--ui-urgency': '0.98' })
    expect(deadlineStyle(deadlineOf(inMs(6 * DAY), NOW))).toBeUndefined()
    expect(deadlineStyle(deadlineOf(inMs(-HOUR), NOW))).toBeUndefined()
  })
})
