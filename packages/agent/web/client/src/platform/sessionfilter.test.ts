import { describe, expect, it } from 'vitest'
import type { SessionInfo } from './ctrlproto/types'
import { filterSessions, sessionLabel } from './sessionfilter'

const s = (id: string, title = '', more: Partial<SessionInfo> = {}): SessionInfo => ({ id, title, ...more }) as SessionInfo
const ids = (rows: { session: SessionInfo }[]) => rows.map((r) => r.session.id)

describe('filterSessions', () => {
  const list = [
    s('a', 'Refactor the settings surface'),
    s('b', 'Fix the flaky procgroup test', { waiting: 'approval' }),
    s('c', 'Talkoot kickoff race (flake)'),
    s('d', 'Disk usage report, fleet sweep', { origin: 'neot' }),
    s('e', 'Answer the port question', { waiting: 'question' }),
  ]

  it('lists every session with no query, waiting first, each part in list order', () => {
    const got = filterSessions(list, '')
    expect(ids(got.waiting)).toEqual(['b', 'e'])
    expect(ids(got.recent)).toEqual(['a', 'c', 'd'])
    expect(got.matched).toBe(5)
    // Nothing to mark without a query.
    expect(got.waiting.every((r) => r.match === null)).toBe(true)
  })

  it('keeps the titles that contain the query, ignoring case, and marks the span', () => {
    const got = filterSessions(list, '  FL ')
    expect(ids(got.waiting)).toEqual(['b'])
    expect(ids(got.recent)).toEqual(['c', 'd'])
    expect(got.matched).toBe(3)
    expect(got.waiting[0].match).toEqual([8, 10])
    expect(sessionLabel(got.waiting[0].session).slice(8, 10)).toBe('fl')
  })

  it('matches the id of a session that has no title yet', () => {
    const got = filterSessions([s('20261011-090000-1a2b3c4d')], '1a2b')
    expect(ids(got.recent)).toEqual(['20261011-090000-1a2b3c4d'])
    expect(got.recent[0].match).toEqual([16, 20])
  })

  it('caps the recent rows and never a waiting one', () => {
    const many = Array.from({ length: 10 }, (_, i) => s(`r${i}`, `row ${i}`))
    many.push(s('w', 'row waits', { waiting: 'approval' }))
    const got = filterSessions(many, 'row', 4)
    expect(ids(got.waiting)).toEqual(['w'])
    expect(ids(got.recent)).toEqual(['r0', 'r1', 'r2'])
    // The count still says how many matched, so the palette can say it cut.
    expect(got.matched).toBe(11)
  })

  it('keeps a match it cannot place, unmarked', () => {
    // İ lower-cases to two code units, so an index into the lower-cased copy
    // would point one place late in the label.
    const got = filterSessions([s('x', 'İstanbul trip')], 'trip')
    expect(ids(got.recent)).toEqual(['x'])
    expect(got.recent[0].match).toBeNull()
  })
})
