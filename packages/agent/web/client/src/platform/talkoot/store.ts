import type {
  TalkootCard,
  TalkootLine,
  TalkootMember,
  TalkootMemberStatus,
  TalkootRoomPage,
  TalkootView,
  WireEvent,
} from '../ctrlproto/types'

// The state of one talkoot as the web view holds it: the roster and each
// member's status, the room lines it has read, and the inbox. Pure functions
// only. The hook in features/talkoot drives them from the wire.
//
// 🔑 Every view of a talkoot is a filter over one room log: the room itself,
// the coordinator's conversation, and each member's conversation with the
// person. So the state keeps the lines once, and the filters below derive the
// rest.

export interface TalkootState {
  id: string
  view: TalkootView | null
  // Oldest first. Lines arrive by page (talkoot.room) and by event; lineKey
  // drops a line that both delivered.
  lines: TalkootLine[]
  // The `before` of the next older page, or undefined when the room start is
  // loaded.
  next?: number
  // Whether a page has been read. Until one has, an undefined next means
  // nothing is known yet, not that the room start is loaded.
  paged: boolean
  cards: TalkootCard[]
  // rosterGen counts roster lines, and viewGen is the count the held roster
  // was read at. The member list is stale while rosterGen is ahead, so a
  // second roster change during a refresh still triggers another.
  rosterGen: number
  viewGen: number
}

export function emptyTalkoot(id: string): TalkootState {
  return { id, view: null, lines: [], paged: false, cards: [], rosterGen: 0, viewGen: 0 }
}

// HUMAN_PREFIX marks a person in an envelope's from and to.
export const HUMAN_PREFIX = 'human:'

export function isPerson(addr: string): boolean {
  return addr.startsWith(HUMAN_PREFIX)
}

// lineKey identifies a room line, so a line that a page and an event both
// delivered is kept once. An envelope has its own id; any other line is its
// type, time, member, and ref.
export function lineKey(l: TalkootLine): string {
  if (l.envelope) return `e:${l.envelope.id}`
  return `${l.type}:${l.at}:${l.member ?? ''}:${l.ref ?? ''}:${l.chain ?? ''}`
}

// appendLines adds event lines. An event is almost always the newest line,
// and then it goes at the end. One buffered through a load can be older than
// the page the load read, when more than a page arrived in between, so a line
// older than the last one held is sorted into place.
function appendLines(have: TalkootLine[], add: TalkootLine[]): TalkootLine[] {
  if (add.length === 0) return have
  const seen = new Set(have.map(lineKey))
  const fresh = add.filter((l) => !seen.has(lineKey(l)))
  if (fresh.length === 0) return have
  const last = have[have.length - 1]
  const inOrder = !last || fresh.every((l) => compareTime(l.at, last.at) >= 0)
  return inOrder ? [...have, ...fresh] : byTime([...have, ...fresh])
}

// mergePage folds a page of the room. An older page goes in front of what is
// held. The newest page, read on open and again after each reconnect, merges
// by time: events that arrived while the page was in flight, and lines held
// from before a reconnect, keep their place.
export function mergePage(s: TalkootState, page: TalkootRoomPage, older: boolean): TalkootState {
  const seen = new Set(s.lines.map(lineKey))
  const fresh = page.lines.filter((l) => !seen.has(lineKey(l)))
  if (older) return { ...s, lines: [...fresh, ...s.lines], next: page.next, paged: true }
  // 🚨 After a reconnect, the newest page may not reach back to the lines
  // held from before it: more than a page arrived while the socket was down.
  // Merging would leave a gap that no older page fills, because the cursor
  // points before the held lines. So a page that shares no line with what is
  // held, and has older lines behind it, replaces the held lines, and the
  // older ones load on demand.
  const overlaps = fresh.length < page.lines.length
  if (s.paged && s.lines.length > 0 && page.lines.length > 0 && !overlaps && page.next !== undefined) {
    return { ...s, lines: byTime(page.lines), next: page.next, paged: true }
  }
  const lines = byTime([...s.lines, ...fresh])
  // The cursor stays at the oldest page held. A room already read to its
  // start keeps no cursor.
  let next = page.next
  if (s.paged) next = s.next === undefined || page.next === undefined ? s.next : Math.min(s.next, page.next)
  return { ...s, lines, next, paged: true }
}

// byTime sorts lines by their time and keeps the order of lines with the
// same time.
function byTime(lines: TalkootLine[]): TalkootLine[] {
  return lines
    .map((l, i) => ({ l, i }))
    .sort((a, b) => compareTime(a.l.at, b.l.at) || a.i - b.i)
    .map((x) => x.l)
}

// compareTime orders two RFC 3339 times to the nanosecond.
//
// ⚠️ Date.parse keeps milliseconds, and Go writes nanoseconds, so two lines in
// one millisecond would compare equal. Compare whole seconds, then the
// fraction. Text comparison fails too: Go trims the fraction, so "…:05Z"
// sorts after "…:05.5Z" as a string.
export function compareTime(a: string, b: string): number {
  const x = timeParts(a)
  const y = timeParts(b)
  return x[0] - y[0] || x[1] - y[1]
}

const RFC3339 = /^(.*T\d\d:\d\d:\d\d)(?:\.(\d+))?(Z|[+-]\d\d:\d\d)$/

function timeParts(at: string): [number, number] {
  const m = RFC3339.exec(at)
  if (!m) return [Date.parse(at) || 0, 0]
  return [Date.parse(m[1] + m[3]) || 0, Number((m[2] ?? '').padEnd(9, '0').slice(0, 9))]
}

// rosterStale reports whether a roster line arrived after the held roster
// was read.
export function rosterStale(s: TalkootState): boolean {
  return s.rosterGen > s.viewGen
}

// setView installs a roster read at roster generation gen. A load, the first
// or one after a reconnect, takes every status from the snapshot, because
// events were missed while the socket was down. A refresh on a live
// subscription (keepStatus) keeps the status the held view has for a member
// it already knows: the status events keep that current, and the snapshot
// may be older than the newest of them. A read from before the held view's
// generation is dropped: two refreshes can answer out of order.
export function setView(s: TalkootState, view: TalkootView, gen = s.rosterGen, keepStatus = false): TalkootState {
  if (gen < s.viewGen) return s
  let next = view
  if (keepStatus && s.view) {
    const held = new Map(s.view.members.map((m) => [m.id, m.status]))
    next = { ...view, members: view.members.map((m) => (held.has(m.id) ? { ...m, status: held.get(m.id)! } : m)) }
  }
  return { ...s, view: next, viewGen: Math.max(s.viewGen, gen) }
}

export function setCards(s: TalkootState, cards: TalkootCard[]): TalkootState {
  return { ...s, cards }
}

function cardKey(c: TalkootCard): string {
  return `${c.kind}:${c.session}:${c.id}`
}

function withStatus(members: TalkootMember[], statuses: TalkootMemberStatus[]): TalkootMember[] {
  const by = new Map(statuses.map((st) => [st.member, st]))
  return members.map((m) => {
    const st = by.get(m.id)
    return st ? { ...m, status: st } : m
  })
}

// applyTalkootEvent folds one event from the talkoot's address. An event for
// another talkoot, or of another type, returns s unchanged.
export function applyTalkootEvent(s: TalkootState, ev: WireEvent): TalkootState {
  const t = ev.talkoot
  if (!t || t.id !== s.id) return s
  switch (ev.type) {
    case 'talkoot_envelope':
    case 'talkoot_intro':
    case 'talkoot_answer':
      return t.line ? { ...s, lines: appendLines(s.lines, [t.line]) } : s
    case 'talkoot_roster':
      return { ...s, lines: t.line ? appendLines(s.lines, [t.line]) : s.lines, rosterGen: s.rosterGen + 1 }
    case 'talkoot_status':
      if (!t.members || !s.view) return s
      return { ...s, view: { ...s.view, members: withStatus(s.view.members, t.members) } }
    case 'talkoot_inbox': {
      if (!t.card) return s
      const key = cardKey(t.card)
      return { ...s, cards: [...s.cards.filter((c) => cardKey(c) !== key), t.card] }
    }
    case 'talkoot_inbox_resolved': {
      if (!t.card) return s
      const key = cardKey(t.card)
      const cards = s.cards.filter((c) => cardKey(c) !== key)
      return cards.length === s.cards.length ? s : { ...s, cards }
    }
  }
  return s
}

// Presence is a member's one state (docs/proposals/talkoot.md, "Presence").
// The wire carries working and paused. Waiting is a card in the inbox that
// names the member. The daemon does not report stopped or failed yet.
export type Presence = 'idle' | 'working' | 'waiting' | 'paused'

export function presence(m: TalkootMember, cards: TalkootCard[]): Presence {
  if (m.status?.paused) return 'paused'
  if (cards.some((c) => c.member === m.id && (c.kind === 'ask' || c.kind === 'permission'))) return 'waiting'
  if (m.status?.working) return 'working'
  return 'idle'
}

export function coordinator(view: TalkootView | null): TalkootMember | undefined {
  return view?.members.find((m) => m.role === 'coordinator')
}

// forPerson matches the address of the person using the view. With no name
// set, the view only watches, so every person matches.
function forPerson(person: string): (addr: string) => boolean {
  if (!person) return isPerson
  const me = HUMAN_PREFIX + person
  return (addr) => addr === me
}

// conversation is a member's conversation with the person using the view:
// every envelope from that person to the member, and from the member to that
// person. An envelope with no `to` from a person reaches the coordinator,
// which the router already wrote into `to`. Another person's exchange with
// the member shows in the room, not here.
export function conversation(lines: TalkootLine[], member: string, person = ''): TalkootLine[] {
  const mine = forPerson(person)
  return lines.filter((l) => {
    const e = l.envelope
    if (!e) return (l.type === 'intro' || l.type === 'answer') && l.member === member
    if (isPerson(e.from)) return mine(e.from) && e.to.includes(member)
    return e.from === member && e.to.some(mine)
  })
}

// A RoomItem is one entry of the room view: a line, or a run of teammate
// envelopes between the same two members collapsed to one entry.
export type RoomItem =
  | { kind: 'line'; line: TalkootLine }
  | { kind: 'exchange'; members: [string, string]; lines: TalkootLine[] }

function teammatePair(l: TalkootLine): [string, string] | null {
  const e = l.envelope
  if (!e || isPerson(e.from) || e.to.length !== 1 || isPerson(e.to[0])) return null
  const pair = [e.from, e.to[0]].sort() as [string, string]
  return pair
}

// roomItems collapses consecutive envelopes between the same two members into
// one exchange. A line that is not an envelope, or an envelope that involves a
// person or more than one recipient, stands alone, and so does a lone
// teammate message: there is nothing to collapse. Lines the room records for
// its own bookkeeping (delivery, read, seat, turn) are left out.
export function roomItems(lines: TalkootLine[]): RoomItem[] {
  const out: RoomItem[] = []
  for (const l of lines) {
    if (HIDDEN_LINES.has(l.type)) continue
    const pair = teammatePair(l)
    const last = out[out.length - 1]
    if (pair) {
      if (last && last.kind === 'exchange' && last.members[0] === pair[0] && last.members[1] === pair[1]) {
        last.lines.push(l)
        continue
      }
      out.push({ kind: 'exchange', members: pair, lines: [l] })
      continue
    }
    out.push({ kind: 'line', line: l })
  }
  return out.map((it) => (it.kind === 'exchange' && it.lines.length === 1 ? { kind: 'line', line: it.lines[0] } : it))
}

const HIDDEN_LINES = new Set(['delivery', 'read', 'seat', 'turn'])

// unreadFor counts the envelopes from member to the person newer than seenAt,
// the time of the last line the person saw in that member's view. A time,
// not an index, because an older page loaded later shifts every index.
//
// ⚠️ Compare with compareTime, not as text and not with Date.parse.
export function unreadFor(lines: TalkootLine[], member: string, seenAt: string, person = ''): number {
  const mine = forPerson(person)
  let n = 0
  for (const l of lines) {
    const e = l.envelope
    if (e && e.from === member && e.to.some(mine) && (!seenAt || compareTime(l.at, seenAt) > 0)) n++
  }
  return n
}
