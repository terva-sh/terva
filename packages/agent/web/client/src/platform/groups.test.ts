import { describe, expect, it } from 'vitest'
import type { Group, SessionInfo } from './ctrlproto/types'
import { SYS_STAGE, SYS_UNGROUPED, applyGroupFilter, bulkMembers, bulkState, cycleGroup, emptyFilter, groupState, originGroups, pruneFilter, setExcluded, stageSystemGroup, ungroupedGroup, worldGroups } from './groups'

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


// Bulk membership: one selection, one request per group.
describe('bulkState', () => {
  const g: Group = { id: 'g', name: 'Ready', members: ['a', 'b'] }

  it('reports how the whole selection sits in the group', () => {
    expect(bulkState(g, new Set(['a', 'b']))).toBe('all')
    expect(bulkState(g, new Set(['a', 'c']))).toBe('some')
    expect(bulkState(g, new Set(['c', 'd']))).toBe('none')
  })

  it('calls an empty selection "none", not vacuously "all"', () => {
    // Every chip would otherwise claim to hold a selection that does not exist.
    expect(bulkState(g, new Set())).toBe('none')
    expect(bulkState({ id: 'e', name: 'Empty', members: [] }, new Set())).toBe('none')
  })

  it('ignores members outside the selection', () => {
    expect(bulkState({ id: 'g', name: 'Big', members: ['a', 'b', 'z'] }, new Set(['a', 'b']))).toBe('all')
  })
})

describe('bulkMembers', () => {
  it('adds the whole selection in one list', () => {
    expect(bulkMembers(['a'], ['b', 'c'], true)).toEqual(['a', 'b', 'c'])
  })

  it('removes the whole selection in one list', () => {
    expect(bulkMembers(['a', 'b', 'c'], ['a', 'c'], false)).toEqual(['b'])
  })

  it('does not shuffle a card that is already a member to the end', () => {
    // Member order is what a group's own sheet lists them in, so re-adding an
    // existing member must be a no-op rather than a reorder.
    expect(bulkMembers(['a', 'b', 'c'], ['a'], true)).toEqual(['a', 'b', 'c'])
    expect(bulkMembers(['a', 'b', 'c'], ['c', 'd'], true)).toEqual(['a', 'b', 'c', 'd'])
  })

  it('collapses duplicates inside the selection', () => {
    expect(bulkMembers([], ['a', 'a', 'b'], true)).toEqual(['a', 'b'])
  })

  it('leaves the list alone when the selection is not in it', () => {
    expect(bulkMembers(['a', 'b'], ['z'], false)).toEqual(['a', 'b'])
  })

  it('never mutates the list it was given', () => {
    const current = ['a', 'b']
    bulkMembers(current, ['c'], true)
    expect(current).toEqual(['a', 'b'])
  })
})

// The "Ungrouped" chip is what makes a flat import finishable: it turns the
// pile into a queue that visibly shrinks as cards are filed.
describe('ungroupedGroup', () => {
  const items = [{ id: 'a' }, { id: 'b' }, { id: 'c' }]
  const idOf = (i: { id: string }) => i.id

  it('holds exactly what no user group holds', () => {
    const groups: Group[] = [{ id: 'g1', name: 'Fantasy', members: ['a'] }]
    expect(ungroupedGroup(items, groups, 'Ungrouped', idOf).members).toEqual(['b', 'c'])
  })

  it('shrinks as cards are filed — the progress the count reports', () => {
    const before: Group[] = [{ id: 'g1', name: 'Fantasy', members: [] }]
    const after: Group[] = [{ id: 'g1', name: 'Fantasy', members: ['a', 'b'] }]
    expect(ungroupedGroup(items, before, 'Ungrouped', idOf).members).toHaveLength(3)
    expect(ungroupedGroup(items, after, 'Ungrouped', idOf).members).toHaveLength(1)
  })

  it('counts membership across ALL user groups, not just the first', () => {
    const groups: Group[] = [
      { id: 'g1', name: 'Fantasy', members: ['a'] },
      { id: 'g2', name: 'Sci-fi', members: ['b'] },
    ]
    expect(ungroupedGroup(items, groups, 'Ungrouped', idOf).members).toEqual(['c'])
  })

  // A system group is derived from what an item already IS, not from anything
  // the user filed. Counting one would mark cards done that nobody touched —
  // and a derived group covering everything would empty the queue outright.
  it('ignores system groups when deciding what is filed', () => {
    const groups: Group[] = [{ id: 'sys:everything', name: 'All', members: ['a', 'b', 'c'], system: true }]
    expect(ungroupedGroup(items, groups, 'Ungrouped', idOf).members).toEqual(['a', 'b', 'c'])
  })

  // Returned even when empty: applyGroupFilter drops a filter naming a group
  // that does not exist, so a vanishing chip would flood the whole library back
  // the instant the last card was filed.
  it('still exists when everything has been filed', () => {
    const groups: Group[] = [{ id: 'g1', name: 'All', members: ['a', 'b', 'c'] }]
    const g = ungroupedGroup(items, groups, 'Ungrouped', idOf)
    expect(g.id).toBe(SYS_UNGROUPED)
    expect(g.members).toEqual([])
  })

  it('filters through the ordinary include machinery, needing no special case', () => {
    const groups: Group[] = [{ id: 'g1', name: 'Fantasy', members: ['a'] }]
    const ung = ungroupedGroup(items, groups, 'Ungrouped', idOf)
    const visible = applyGroupFilter(items, [...groups, ung], { include: [SYS_UNGROUPED], exclude: [] }, idOf)
    expect(visible.map(idOf)).toEqual(['b', 'c'])
  })

  it('is marked system, so its chip carries no edit affordance', () => {
    expect(ungroupedGroup(items, [], 'Ungrouped', idOf).system).toBe(true)
  })
})

describe('worldGroups', () => {
  type C = { id: string; world?: string }
  const NAMES: Record<string, string> = { 'w-1': 'Bellhaven', 'w-2': 'Lowtown' }
  const nameOf = (id: string) => NAMES[id] ?? ''
  const of = (items: C[]) => worldGroups(items, (c) => c.id, (c) => c.world, nameOf)

  it('makes one chip per World, named and sorted', () => {
    const got = of([{ id: 'a', world: 'w-2' }, { id: 'b', world: 'w-1' }, { id: 'c' }])
    expect(got.map((g) => g.name)).toEqual(['Bellhaven', 'Lowtown'])
    expect(got[0].members).toEqual(['b'])
  })

  it('marks them system, so they filter but cannot be edited', () => {
    // A chip whose membership is a FACT about the cards has nothing to rename,
    // recolour, or hand-file into — and letting the UI offer that would promise
    // an edit the next listing silently discards.
    expect(of([{ id: 'a', world: 'w-1' }]).every((g) => g.system)).toBe(true)
  })

  it('leaves out cards that belong to no World', () => {
    expect(of([{ id: 'a' }, { id: 'b' }])).toEqual([])
  })

  // The World listing arrives on its own fetch, so there is a beat where a card
  // knows its World id and nothing knows the name. A chip reading
  // "lowtown-abc123" is noise the author cannot act on.
  it('skips a World whose name has not loaded yet', () => {
    expect(of([{ id: 'a', world: 'w-unknown' }])).toEqual([])
  })
})
