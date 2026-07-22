// Group filtering + derived (system) groups.
//
// A group is a membership bucket the library browses by (platform/ctrlproto
// Group). Most are user-curated and stored server-side; a few are SYSTEM groups
// synthesized here from session metadata — the `stage` bucket and per-card /
// per-World origins — so they cost no writes, never grow a doc, and can't be
// hand-edited. The daemon never hears about them.
//
// The filter model is the standard faceted one: pick include groups to narrow
// to their UNION (or everything when none are picked), then subtract anything in
// any exclude group. Exclude wins over include. This module is pure so the rule
// is unit-tested (vitest runs in CI; see groups.test.ts).

import type { Group, SessionInfo } from './ctrlproto/types'

// The reserved id of the single `stage` system group — every immersive session.
// The control panel excludes it by default so play sessions don't clutter the
// coding-session list. `sys:` ids can never collide with a stored group's id.
export const SYS_STAGE = 'sys:stage'

// GroupFilter is include/exclude by group id. include empty ⇒ every item is in
// scope; a non-empty include narrows to the union of those groups. exclude then
// subtracts anything in any listed group. Both are plain id lists so the filter
// serialises straight into a URL param or localStorage.
export interface GroupFilter {
  include: string[]
  exclude: string[]
}

export const emptyFilter: GroupFilter = { include: [], exclude: [] }

export type GroupState = 'off' | 'include' | 'exclude'

// groupState reports how a chip should render: excluded wins over included so a
// group that somehow lands in both reads as hidden (matching applyGroupFilter).
export function groupState(f: GroupFilter, id: string): GroupState {
  if (f.exclude.includes(id)) return 'exclude'
  if (f.include.includes(id)) return 'include'
  return 'off'
}

// cycleGroup advances one chip through off → include → exclude → off, returning a
// fresh filter (the id is first cleared from both lists so a state is never
// duplicated). This is the whole interaction model for a tri-state filter chip.
export function cycleGroup(f: GroupFilter, id: string): GroupFilter {
  const state = groupState(f, id)
  const include = f.include.filter((x) => x !== id)
  const exclude = f.exclude.filter((x) => x !== id)
  if (state === 'off') return { include: [...include, id], exclude }
  if (state === 'include') return { include, exclude: [...exclude, id] }
  return { include, exclude } // exclude → off
}

// setExcluded forces one group into or out of the exclude list without touching
// include — the panel's "hide Stage sessions" toggle uses it directly.
export function setExcluded(f: GroupFilter, id: string, excluded: boolean): GroupFilter {
  const exclude = f.exclude.filter((x) => x !== id)
  return {
    include: f.include.filter((x) => x !== id),
    exclude: excluded ? [...exclude, id] : exclude,
  }
}

// hasFilter is true when the filter actually narrows anything (an empty filter
// returns every item, so callers can skip the "no results" affordance).
export function hasFilter(f: GroupFilter): boolean {
  return f.include.length > 0 || f.exclude.length > 0
}

// applyGroupFilter returns the items still visible under the filter:
//   visible = (include empty ? all : in ANY include group) AND NOT in ANY exclude group
// idOf maps an item to the id its groups store as a member.
//
// A filter id with no live group is dropped before applying, so the filter is
// self-healing: an INCLUDE of a since-deleted group (or a derived origin chip
// whose last chat was removed) degrades to "show all" rather than an empty
// screen, and a stale EXCLUDE simply subtracts nothing. This is also why a
// persisted `sys:stage` exclude can safely outlive a moment with zero Stage
// sessions — it re-engages the instant one exists again.
export function applyGroupFilter<T>(
  items: T[],
  groups: Group[],
  f: GroupFilter,
  idOf: (item: T) => string,
): T[] {
  if (!hasFilter(f)) return items
  const memberOf = new Map<string, Set<string>>()
  for (const g of groups) memberOf.set(g.id, new Set(g.members))
  const include = f.include.filter((id) => memberOf.has(id))
  const exclude = f.exclude.filter((id) => memberOf.has(id))
  if (include.length === 0 && exclude.length === 0) return items
  const inAny = (ids: string[], id: string) => ids.some((gid) => memberOf.get(gid)!.has(id))
  return items.filter((it) => {
    const id = idOf(it)
    if (exclude.length > 0 && inAny(exclude, id)) return false
    if (include.length > 0 && !inAny(include, id)) return false
    return true
  })
}

// prune drops from a filter any id no longer present in the live group list
// (e.g. an origin chip whose last chat was deleted, or a user group removed on
// another client), so a stale id can't silently keep hiding things forever. A
// referenced system id that's still derivable is kept by passing it in ids.
export function pruneFilter(f: GroupFilter, ids: Set<string>): GroupFilter {
  const keep = (list: string[]) => list.filter((id) => ids.has(id))
  const include = keep(f.include)
  const exclude = keep(f.exclude)
  if (include.length === f.include.length && exclude.length === f.exclude.length) return f
  return { include, exclude }
}

// isStageSession is the one predicate for "this is a Stage session": any
// non-empty experience ('chat' | 'play'). Coding sessions have none.
export function isStageSession(s: SessionInfo): boolean {
  return !!s.experience
}

// stageSystemGroup collects every immersive session into the single `stage`
// bucket, or null when there are none. label is passed in (i18n stays in the
// component); members are live session ids, so applyGroupFilter needs no extra
// liveness pass. system:true gates the edit affordances off.
export function stageSystemGroup(sessions: SessionInfo[], label: string): Group | null {
  const members = sessions.filter(isStageSession).map((s) => s.id)
  if (members.length === 0) return null
  return { id: SYS_STAGE, name: label, members, system: true }
}

// originGroups derives one system group per distinct origin a Stage chat came
// from — the bound World when set, otherwise the seeding card — keyed so a World
// and a card can't collide. Only origins with a live chat are returned (the
// "chips only when they have sessions" rule), and a chat with neither is skipped.
// nameOf resolves the display label for a given kind+id; a missing name falls
// back to the id so a chip is never blank.
export function originGroups(
  sessions: SessionInfo[],
  nameOf: (kind: 'world' | 'card', id: string) => string | undefined,
): Group[] {
  const byId = new Map<string, { name: string; members: string[] }>()
  for (const s of sessions) {
    if (!isStageSession(s)) continue
    let kind: 'world' | 'card'
    let ref: string
    if (s.world) {
      kind = 'world'
      ref = s.world
    } else if (s.card) {
      kind = 'card'
      ref = s.card
    } else {
      continue
    }
    const id = `sys:${kind}:${ref}`
    let entry = byId.get(id)
    if (!entry) {
      entry = { name: nameOf(kind, ref) || ref, members: [] }
      byId.set(id, entry)
    }
    entry.members.push(s.id)
  }
  return [...byId.entries()].map(([id, e]) => ({ id, name: e.name, members: e.members, system: true }))
}
