import { describe, expect, it } from 'vitest'
import type { SessionInfo } from './ctrlproto/types'
import { IDLE_WINDOW_MS, coldStartsOpen, sectionSessions, sessionStatus } from './sessionstatus'

const NOW = Date.parse('2026-07-28T12:00:00Z')
const ago = (ms: number) => new Date(NOW - ms).toISOString()

const session = (o: Partial<SessionInfo> = {}): SessionInfo => ({
  id: 's1',
  messages: 2,
  usage: { input: 0, output: 0, cache_read: 0, cache_write: 0, cost_usd: 0 },
  ...o,
})

describe('sessionStatus', () => {
  it('reads a turn in flight as busy, from the list or from a subscription', () => {
    expect(sessionStatus(session({ busy: true, live: true }), undefined, NOW)).toBe('busy')
    expect(sessionStatus(session({ live: true }), true, NOW)).toBe('busy')
  })

  it('lets a subscription overrule the list in BOTH directions', () => {
    // The list is a point-in-time scan; the stream is at turn latency. A tile
    // that has just gone quiet must not stay busy until the next poll, and one
    // that has just started must not wait for it either.
    expect(sessionStatus(session({ busy: true, live: true }), false, NOW)).toBe('idle')
    expect(sessionStatus(session({ live: true }), true, NOW)).toBe('busy')
  })

  it('treats being subscribed at all as proof the session is live', () => {
    // No live flag, no recent mtime — but something is streaming it, so it is
    // materialized by definition. ABSENT and `false` are different answers here.
    expect(sessionStatus(session({ updated: ago(30 * 24 * 60 * 60 * 1000) }), false, NOW)).toBe('idle')
    expect(sessionStatus(session({ updated: ago(30 * 24 * 60 * 60 * 1000) }), undefined, NOW)).toBe('cold')
  })

  it('rescues a forgotten session while its transcript is recent', () => {
    // The shape this exists for: the daemon restarted, so nothing is live, but
    // you were in this five minutes ago and it must not be collapsed away.
    expect(sessionStatus(session({ updated: ago(5 * 60 * 1000) }), undefined, NOW)).toBe('idle')
  })

  it('holds the window boundary from both sides', () => {
    expect(sessionStatus(session({ updated: ago(IDLE_WINDOW_MS - 1000) }), undefined, NOW)).toBe('idle')
    expect(sessionStatus(session({ updated: ago(IDLE_WINDOW_MS + 1000) }), undefined, NOW)).toBe('cold')
  })

  it('does not invent a timestamp when the daemon sent none', () => {
    // An older daemon omits `updated` entirely. Reading absent as "now" would
    // rescue every session and empty the cold group — the degradation has to be
    // to the live-state-only answer this surface gave before the window existed.
    expect(sessionStatus(session(), undefined, NOW)).toBe('cold')
    expect(sessionStatus(session({ updated: 'not a date' }), undefined, NOW)).toBe('cold')
    expect(sessionStatus(session({ live: true }), undefined, NOW)).toBe('idle')
  })
})

describe('sectionSessions', () => {
  it('splits the list and keeps the daemon’s mtime-descending order', () => {
    const list = [
      session({ id: 'cold-new' }),
      session({ id: 'warm', updated: ago(60 * 1000) }),
      session({ id: 'working', live: true, busy: true }),
      session({ id: 'cold-old' }),
      session({ id: 'held', live: true }),
    ]
    const s = sectionSessions(list, undefined, NOW)
    expect(s.busy.map((x) => x.id)).toEqual(['working'])
    expect(s.idle.map((x) => x.id)).toEqual(['warm', 'held'])
    expect(s.cold.map((x) => x.id)).toEqual(['cold-new', 'cold-old'])
  })

  it('keys the streamed flags by session id', () => {
    const list = [session({ id: 'a' }), session({ id: 'b' })]
    const s = sectionSessions(list, { a: true }, NOW)
    expect(s.busy.map((x) => x.id)).toEqual(['a'])
    expect(s.cold.map((x) => x.id)).toEqual(['b'])
  })
})

describe('coldStartsOpen', () => {
  it('opens cold only when it is the whole list', () => {
    expect(coldStartsOpen(sectionSessions([session({ id: 'a' })], undefined, NOW))).toBe(true)
    expect(coldStartsOpen(sectionSessions([], undefined, NOW))).toBe(true)
    expect(coldStartsOpen(sectionSessions([session({ id: 'a' }), session({ id: 'b', live: true })], undefined, NOW))).toBe(false)
    expect(coldStartsOpen(sectionSessions([session({ id: 'a', busy: true })], undefined, NOW))).toBe(false)
  })
})
