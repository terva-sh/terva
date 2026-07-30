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

// The reserved id of the "Ungrouped" system group: everything that is in no
// user group yet. Derived like the others, so it needs no verb and no storage.
export const SYS_UNGROUPED = 'sys:ungrouped'

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

// BulkState is how a whole selection sits with respect to one group: none of it
// is in, some of it is, or all of it is. It is what lets a single control mean
// "add these" or "remove these" without a second one beside it.
export type BulkState = 'none' | 'some' | 'all'

// bulkState reports how `ids` sit in `g`. An EMPTY selection is 'none' — there
// is nothing in the group, and calling it 'all' (vacuously true) would render a
// bar whose every chip claims to hold a selection that does not exist.
export function bulkState(g: Group, ids: Set<string>): BulkState {
  if (ids.size === 0) return 'none'
  const members = new Set(g.members)
  let inside = 0
  for (const id of ids) if (members.has(id)) inside++
  if (inside === 0) return 'none'
  return inside === ids.size ? 'all' : 'some'
}

// bulkMembers returns a group's new member list after adding or removing a
// whole selection at once.
//
// The membership verb REPLACES the list, so a bulk edit is ONE request per
// group rather than one per card — which is also what makes it all-or-nothing:
// either the whole selection lands on the shelf or none of it does.
//
// Adding appends only the genuinely new ids and preserves the existing order,
// so re-adding a card already in the group does not shuffle it to the end (that
// order is what a group's own sheet lists members in). Duplicates inside `ids`
// collapse.
export function bulkMembers(current: string[], ids: string[], add: boolean): string[] {
  if (!add) {
    const drop = new Set(ids)
    return current.filter((m) => !drop.has(m))
  }
  const seen = new Set(current)
  const out = [...current]
  for (const id of ids) {
    if (seen.has(id)) continue
    seen.add(id)
    out.push(id)
  }
  return out
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

// ungroupedGroup synthesizes the "Ungrouped" chip: every item that no USER
// group holds. Always returned, even with no members.
//
// It exists for the case a library is hardest to face — an import that arrives
// as one flat list of hundreds, all of it needing to be filed. Sorting and
// searching help you find a card; neither tells you which ones you have already
// dealt with. Filtering to this group turns the pile into a work queue that
// visibly shrinks: file a batch, and they leave the view. The chip's own count
// is the progress bar.
//
// label is passed in, like stageSystemGroup — i18n stays in the component.
//
// SYSTEM groups are not "grouped". They are derived from what an item already
// is (a session's origin), so counting them would mark items as filed that the
// user never filed — and for a derived group that covers everything, the queue
// would read empty before any work was done.
//
// Returned even when empty on purpose. A filter naming a group that no longer
// exists degrades to "show everything" (see applyGroupFilter), which for this
// chip would mean the moment you file the last card, the whole library floods
// back — the opposite of the "you're done" the empty queue should report.
export function ungroupedGroup<T>(items: T[], groups: Group[], label: string, idOf: (item: T) => string): Group {
  const filed = new Set<string>()
  for (const g of groups) {
    if (g.system) continue
    for (const m of g.members) filed.add(m)
  }
  return {
    id: SYS_UNGROUPED,
    name: label,
    members: items.map(idOf).filter((id) => !filed.has(id)),
    system: true,
  }
}
