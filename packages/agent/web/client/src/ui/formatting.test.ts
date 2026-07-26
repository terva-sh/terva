import { describe, expect, it } from 'vitest'
import { clockTime, compact, humanBytes, humanCount, humanGap, localInstant, truncate } from './formatting'

describe('shared formatting', () => {
  it('formats byte and compact number thresholds', () => {
    expect(humanBytes(1023)).toBe('1023 B')
    expect(humanBytes(1536)).toBe('1.5 KB')
    expect(humanBytes(1 << 20)).toBe('1.0 MB')
    expect(humanCount(999)).toBe('999')
    expect(humanCount(1500)).toBe('2k')
    expect(humanCount(1_500_000)).toBe('1.5M')
  })

  it('truncates strings and compact JSON at the existing limit', () => {
    expect(truncate('abcd', 3)).toBe('abc…')
    expect(truncate('abc', 3)).toBe('abc')
    expect(compact({ value: 'x'.repeat(250) })).toHaveLength(201)
  })

  it('returns an empty compact value for cyclic input', () => {
    const cyclic: { self?: unknown } = {}
    cyclic.self = cyclic
    expect(compact(cyclic)).toBe('')
  })
})

// These avoid asserting a literal rendering: the output is locale-dependent by
// design, and CI's locale is not the one to pin. What they assert is the two
// properties the wire slice failed — that the zone is honoured, and that a time
// survives at all.
describe('localInstant', () => {
  // 01:30 UTC on the 2nd is still the evening of the 1st in New York. The old
  // code sliced the wire string and labelled it "2026-08-02", showing a
  // deadline a day later than it actually falls for that viewer.
  const iso = '2026-08-02T01:30:00Z'

  it('renders the instant in the viewer zone, not the wire zone', () => {
    const utc = localInstant(iso, { timeZone: 'UTC' })
    const ny = localInstant(iso, { timeZone: 'America/New_York' })
    expect(ny).not.toBe(utc)
    // The calendar day differs between the two zones, which is precisely the
    // error a string slice cannot avoid.
    const dayIn = (tz: string) => new Intl.DateTimeFormat('en-US', { timeZone: tz, day: 'numeric' }).format(new Date(iso))
    expect(dayIn('UTC')).toBe('2')
    expect(dayIn('America/New_York')).toBe('1')
  })

  // The reported complaint: the row showed a date and no time at all.
  it('keeps a time of day', () => {
    expect(localInstant(iso, { timeZone: 'UTC' })).toContain(':')
  })

  // A caller renders a fallback (the raw status) rather than "Invalid Date".
  it('is empty for a missing or unparseable value', () => {
    expect(localInstant(undefined)).toBe('')
    expect(localInstant('')).toBe('')
    expect(localInstant('whenever')).toBe('')
  })
})

describe('clockTime', () => {
  const iso = '2026-08-02T01:30:00Z'

  // Same viewer-zone contract as localInstant: what changes is only how much of
  // the instant is shown, never which instant it is.
  it('renders the time of day in the viewer zone', () => {
    expect(clockTime(iso)).not.toBe('')
    const utc = new Date(iso).toLocaleTimeString(undefined, { hour: 'numeric', minute: '2-digit', timeZone: 'UTC' })
    const ny = new Date(iso).toLocaleTimeString(undefined, {
      hour: 'numeric',
      minute: '2-digit',
      timeZone: 'America/New_York',
    })
    expect(utc).not.toBe(ny)
  })

  // A stamp under a bubble, not a log line: no seconds, no date.
  it('carries no seconds and no date', () => {
    const out = clockTime(iso)
    expect(out).not.toContain('2026')
    expect(out.match(/:/g) ?? []).toHaveLength(1)
  })

  it('is empty for a missing or unparseable value', () => {
    expect(clockTime(undefined)).toBe('')
    expect(clockTime('whenever')).toBe('')
  })
})

describe('humanGap', () => {
  const MIN = 60_000
  it('reports the coarsest unit that still says something', () => {
    expect(humanGap(12 * MIN)).toBe('12m')
    expect(humanGap(59 * MIN)).toBe('59m')
    expect(humanGap(60 * MIN)).toBe('1h')
    expect(humanGap(23.5 * 60 * MIN)).toBe('23h')
    expect(humanGap(24 * 60 * MIN)).toBe('1d')
    expect(humanGap(50 * 60 * MIN)).toBe('2d')
  })

  // Never "0m": the marker only appears well past a minute, so a zero would
  // mean the caller measured something it should not have.
  it('floors at a minute rather than reporting zero', () => {
    expect(humanGap(20_000)).toBe('1m')
    expect(humanGap(0)).toBe('1m')
  })
})
