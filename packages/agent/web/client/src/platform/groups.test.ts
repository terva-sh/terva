import { describe, expect, it } from 'vitest'
import type { Group, SessionInfo } from './ctrlproto/types'
import {
  applyGroupFilter,
  cycleGroup,
  emptyFilter,
  groupState,
  originGroups,
  pruneFilter,
  setExcluded,
  stageSystemGroup,
  SYS_STAGE,
} from './groups'

const g = (id: string, members: string[]): Group => ({ id, name: id, members })
// Minimal fixture — cast past SessionInfo's required wire fields the filter never reads.
const s = (id: string, over: Partial<SessionInfo> = {}): SessionInfo => ({ id, ...over }) as SessionInfo

describe('applyGroupFilter', () => {
  const groups = [g('ready', ['a', 'b']), g('wip', ['b', 'c']), g('imported', ['c', 'd'])]
  const items = ['a', 'b', 'c', 'd', 'e'].map((id) => ({ id }))
  const ids = (r: { id: string }[]) => r.map((x) => x.id)

  it('returns everything when the filter is empty', () => {
    expect(ids(applyGroupFilter(items, groups, emptyFilter, (x) => x.id))).toEqual(['a', 'b', 'c', 'd', 'e'])
  })

  it('narrows to the union of the include groups', () => {
    const f = { include: ['ready', 'imported'], exclude: [] }
    expect(ids(applyGroupFilter(items, groups, f, (x) => x.id))).toEqual(['a', 'b', 'c', 'd'])
  })

  it('subtracts anything in any exclude group, from the whole set when no include', () => {
    const f = { include: [], exclude: ['imported'] }
    expect(ids(applyGroupFilter(items, groups, f, (x) => x.id))).toEqual(['a', 'b', 'e'])
  })

  it('lets exclude win over include for an item in both', () => {
    // b is in ready (include) and wip (exclude) → hidden.
    const f = { include: ['ready'], exclude: ['wip'] }
    expect(ids(applyGroupFilter(items, groups, f, (x) => x.id))).toEqual(['a'])
  })

  it('ignores unknown ids in the filter (no members, no effect)', () => {
    const f = { include: [], exclude: ['ghost'] }
    expect(ids(applyGroupFilter(items, groups, f, (x) => x.id))).toEqual(['a', 'b', 'c', 'd', 'e'])
  })

  it('degrades an all-unknown include to "show all", not an empty screen', () => {
    // The only include group was deleted → don't hide everything.
    const f = { include: ['ghost'], exclude: [] }
    expect(ids(applyGroupFilter(items, groups, f, (x) => x.id))).toEqual(['a', 'b', 'c', 'd', 'e'])
  })

  it('keeps live include facets when some are unknown', () => {
    const f = { include: ['ready', 'ghost'], exclude: [] }
    expect(ids(applyGroupFilter(items, groups, f, (x) => x.id))).toEqual(['a', 'b'])
  })
})

describe('cycleGroup / groupState', () => {
  it('cycles off → include → exclude → off', () => {
    let f = emptyFilter
    expect(groupState(f, 'x')).toBe('off')
    f = cycleGroup(f, 'x')
    expect(groupState(f, 'x')).toBe('include')
    f = cycleGroup(f, 'x')
    expect(groupState(f, 'x')).toBe('exclude')
    f = cycleGroup(f, 'x')
    expect(groupState(f, 'x')).toBe('off')
  })

  it('never leaves an id in both lists', () => {
    let f: ReturnType<typeof cycleGroup> = { include: ['x'], exclude: ['x'] }
    // exclude wins for state; cycling from exclude clears both.
    expect(groupState(f, 'x')).toBe('exclude')
    f = cycleGroup(f, 'x')
    expect(f.include).not.toContain('x')
    expect(f.exclude).not.toContain('x')
  })

  it('setExcluded toggles exclude membership without touching other ids', () => {
    let f = { include: ['keep'], exclude: [] as string[] }
    f = setExcluded(f, SYS_STAGE, true)
    expect(f.exclude).toEqual([SYS_STAGE])
    expect(f.include).toEqual(['keep'])
    f = setExcluded(f, SYS_STAGE, false)
    expect(f.exclude).toEqual([])
  })
})

describe('pruneFilter', () => {
  it('drops ids no longer present and keeps live ones', () => {
    const f = { include: ['live'], exclude: ['gone', SYS_STAGE] }
    const pruned = pruneFilter(f, new Set(['live', SYS_STAGE]))
    expect(pruned.include).toEqual(['live'])
    expect(pruned.exclude).toEqual([SYS_STAGE])
  })

  it('returns the same object when nothing changes (stable identity)', () => {
    const f = { include: ['a'], exclude: ['b'] }
    expect(pruneFilter(f, new Set(['a', 'b']))).toBe(f)
  })
})

describe('stageSystemGroup', () => {
  it('collects every immersive session, ignoring coding sessions', () => {
    const sessions = [s('a', { experience: 'chat' }), s('b'), s('c', { experience: 'play' })]
    const grp = stageSystemGroup(sessions, 'Stage')
    expect(grp).toEqual({ id: SYS_STAGE, name: 'Stage', members: ['a', 'c'], system: true })
  })

  it('is null when no session is immersive', () => {
    expect(stageSystemGroup([s('a'), s('b')], 'Stage')).toBeNull()
  })
})

describe('originGroups', () => {
  const nameOf = (kind: 'world' | 'card', id: string): string | undefined => {
    if (kind === 'world' && id === 'neo') return 'Neo-Kyoto'
    if (kind === 'card' && id === 'kob') return 'Kobeni'
    return undefined
  }

  it('groups by World when set, else by card, keyed so they never collide', () => {
    const sessions = [
      s('1', { experience: 'chat', world: 'neo' }),
      s('2', { experience: 'play', world: 'neo' }),
      s('3', { experience: 'chat', card: 'kob' }),
      s('4', { experience: 'chat', world: 'neo', card: 'kob' }), // world wins
    ]
    const out = originGroups(sessions, nameOf).sort((a, b) => a.id.localeCompare(b.id))
    expect(out).toEqual([
      { id: 'sys:card:kob', name: 'Kobeni', members: ['3'], system: true },
      { id: 'sys:world:neo', name: 'Neo-Kyoto', members: ['1', '2', '4'], system: true },
    ])
  })

  it('skips coding sessions and stage sessions with no origin', () => {
    const sessions = [s('1'), s('2', { experience: 'chat' })]
    expect(originGroups(sessions, nameOf)).toEqual([])
  })

  it('falls back to the ref when a name is unknown, never blank', () => {
    const out = originGroups([s('1', { experience: 'chat', card: 'mystery' })], nameOf)
    expect(out[0].name).toBe('mystery')
  })
})
