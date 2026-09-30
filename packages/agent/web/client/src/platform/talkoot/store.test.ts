import { describe, expect, it, vi } from 'vitest'
import type { TalkootCard, TalkootEnvelope, TalkootLine, TalkootMember, TalkootView, WireEvent } from '../ctrlproto/types'
import { TalkootHub } from './hub'
import {
  applyTalkootEvent,
  compareTime,
  conversation,
  emptyTalkoot,
  mergePage,
  presence,
  roomItems,
  rosterStale,
  setView,
  unreadFor,
} from './store'

let n = 0
const env = (from: string, to: string[], at: string, body = 'x'): TalkootLine => {
  const e: TalkootEnvelope = { id: `e${++n}`, talkoot: 'crew', from, to, kind: 'message', body, chain: { root: 'c1', hops: 1 }, at }
  return { type: 'envelope', at, envelope: e }
}

const view = (): TalkootView => ({
  id: 'crew',
  name: 'crew',
  home: '/w',
  members: [
    { id: 'helm', role: 'coordinator', status: { member: 'helm' } },
    { id: 'jev', role: 'specialist', status: { member: 'jev' } },
  ],
})

const ev = (type: string, talkoot: WireEvent['talkoot']): WireEvent => ({ type, talkoot }) as WireEvent

describe('the talkoot store', () => {
  it('keeps a line once when a page and an event both deliver it', () => {
    const a = env('human:sothr', ['helm'], '2026-09-27T10:00:00Z')
    const b = env('helm', ['human:sothr'], '2026-09-27T10:00:05Z')
    let s = emptyTalkoot('crew')
    // The event arrives before the first page answers.
    s = applyTalkootEvent(s, ev('talkoot_envelope', { id: 'crew', line: b }))
    s = mergePage(s, { lines: [a, b], total: 2, next: 40 }, false)
    expect(s.lines.map((l) => l.envelope!.id)).toEqual([a.envelope!.id, b.envelope!.id])
    // An event before the first page must not read as "the room start is loaded".
    expect(s.next).toBe(40)
    expect(s.paged).toBe(true)
  })

  it('keeps the older-page cursor across a reconnect', () => {
    const old = env('helm', ['jev'], '2026-09-27T09:00:00Z')
    const newest = env('jev', ['helm'], '2026-09-27T11:00:00Z')
    let s = mergePage(emptyTalkoot('crew'), { lines: [newest], total: 50, next: 40 }, false)
    s = mergePage(s, { lines: [old], total: 50, next: 10 }, true)
    expect(s.next).toBe(10)
    // The reconnect reads the newest page again. The cursor stays at the oldest page held.
    s = mergePage(s, { lines: [newest], total: 50, next: 40 }, false)
    expect(s.next).toBe(10)
    expect(s.lines).toHaveLength(2)
    expect(s.lines[0]).toBe(old)
  })

  it('replaces the held lines when a reconnect page leaves a gap', () => {
    // Held from before the socket dropped.
    const old = env('helm', ['jev'], '2026-09-27T09:00:00Z')
    let s = mergePage(emptyTalkoot('crew'), { lines: [old], total: 1 }, false)
    expect(s.next).toBeUndefined()
    // More than a page arrived while the socket was down: the newest page
    // shares no line with what is held, and older lines sit behind it.
    const fresh = env('jev', ['helm'], '2026-09-27T12:00:00Z')
    s = mergePage(s, { lines: [fresh], total: 300, next: 200 }, false)
    expect(s.lines).toEqual([fresh])
    expect(s.next).toBe(200)
  })

  it('sorts by time, not by the text of the time', () => {
    // Go trims the fraction, so "...:05Z" sorts after "...:05.5Z" as a string.
    const later = env('helm', ['jev'], '2026-09-27T10:00:05.5Z')
    const earlier = env('jev', ['helm'], '2026-09-27T10:00:05Z')
    const s = mergePage(emptyTalkoot('crew'), { lines: [later, earlier], total: 2 }, false)
    expect(s.lines[0]).toBe(earlier)
    // The person saw the line at :05, and :05.5 is newer. Compared as text,
    // ".5Z" sorts before "Z" and the reply would count as read.
    const reply = env('helm', ['human:sothr'], '2026-09-27T10:00:05.5Z')
    expect(unreadFor([reply], 'helm', '2026-09-27T10:00:05Z')).toBe(1)
  })

  it('sorts an event line older than the held lines into place', () => {
    const page = [env('helm', ['jev'], '2026-09-27T10:00:02Z'), env('jev', ['helm'], '2026-09-27T10:00:03Z')]
    let s = mergePage(emptyTalkoot('crew'), { lines: page, total: 3 }, false)
    const older = env('helm', ['human:sothr'], '2026-09-27T10:00:01Z')
    s = applyTalkootEvent(s, ev('talkoot_envelope', { id: 'crew', line: older }))
    expect(s.lines[0]).toBe(older)
    const newest = env('jev', ['helm'], '2026-09-27T10:00:04Z')
    s = applyTalkootEvent(s, ev('talkoot_envelope', { id: 'crew', line: newest }))
    expect(s.lines[s.lines.length - 1]).toBe(newest)
  })

  it('orders two lines in one millisecond by their nanoseconds', () => {
    const second = env('helm', ['human:sothr'], '2026-09-27T10:00:05.000000900Z')
    const first = env('jev', ['helm'], '2026-09-27T10:00:05.0000001Z')
    const s = mergePage(emptyTalkoot('crew'), { lines: [second, first], total: 2 }, false)
    expect(s.lines[0]).toBe(first)
    expect(unreadFor([second], 'helm', '2026-09-27T10:00:05.0000001Z')).toBe(1)
    expect(compareTime('2026-09-27T12:00:05+02:00', '2026-09-27T10:00:05Z')).toBe(0)
  })

  it('keeps each member\'s newest beat, with a fresh key for every play', () => {
    let s = emptyTalkoot('crew')
    const beat = (member: string, name: string, toward?: string) =>
      ev('talkoot_beat', { id: 'crew', beat: { member, beat: name, toward, cause: 'c', at: '2026-09-30T10:00:00Z' } })
    s = applyTalkootEvent(s, beat('jev', 'glance', 'helm'))
    expect(s.beats.jev).toEqual({ name: 'glance', key: 1, toward: 'helm' })
    s = applyTalkootEvent(s, beat('helm', 'happy'))
    s = applyTalkootEvent(s, beat('jev', 'glance', 'helm'))
    expect(s.beats.jev.key).toBe(3)
    expect(s.beats.helm).toEqual({ name: 'happy', key: 2, toward: undefined })
    // A beat of another talkoot, or one with no member, changes nothing.
    const other = ev('talkoot_beat', { id: 'other', beat: { member: 'jev', beat: 'happy', cause: 'c', at: '2026-09-30T10:00:00Z' } })
    expect(applyTalkootEvent(s, other)).toBe(s)
    expect(applyTalkootEvent(s, ev('talkoot_beat', { id: 'crew' }))).toBe(s)
  })

  it('folds status, inbox, and roster events for its own talkoot only', () => {
    let s = setView(emptyTalkoot('crew'), view())
    s = applyTalkootEvent(s, ev('talkoot_status', { id: 'crew', members: [{ member: 'jev', presence: 'working', working: true }] }))
    expect(s.view!.members[1].status.working).toBe(true)

    const card: TalkootCard = { session: 's1', member: 'jev', kind: 'ask', id: 'a1' }
    s = applyTalkootEvent(s, ev('talkoot_inbox', { id: 'crew', card }))
    expect(s.cards).toHaveLength(1)
    s = applyTalkootEvent(s, ev('talkoot_inbox_resolved', { id: 'crew', card }))
    expect(s.cards).toHaveLength(0)

    s = applyTalkootEvent(s, ev('talkoot_roster', { id: 'crew', line: { type: 'roster', at: '2026-09-27T10:00:00Z', by: 'sothr' } }))
    expect(rosterStale(s)).toBe(true)
    // A refresh read before a second roster line leaves the view stale, and a
    // refresh keeps the live status rather than the snapshot's older one.
    const gen = s.rosterGen
    s = applyTalkootEvent(s, ev('talkoot_roster', { id: 'crew', line: { type: 'roster', at: '2026-09-27T10:00:01Z', by: 'sothr' } }))
    s = setView(s, view(), gen, true)
    expect(rosterStale(s)).toBe(true)
    expect(s.view!.members[1].status.working).toBe(true)
    s = setView(s, view(), s.rosterGen, true)
    expect(rosterStale(s)).toBe(false)
    // A read from before the held generation answers late and is dropped.
    const older = { ...view(), name: 'older' }
    expect(setView(s, older, gen, true).view!.name).not.toBe('older')
    // A load after a reconnect takes the snapshot's status.
    expect(setView(s, view()).view!.members[1].status.working).toBeUndefined()

    const other = applyTalkootEvent(s, ev('talkoot_envelope', { id: 'other', line: env('a', ['b'], '2026-09-27T10:00:00Z') }))
    expect(other).toBe(s)
  })

  it('reads the presence the daemon decided', () => {
    const ask: TalkootCard[] = [{ session: 's', member: 'jev', kind: 'ask', id: 'a' }]
    const at = (status: TalkootMember['status'], cards: TalkootCard[] = []) => presence({ id: 'jev', role: 'specialist', status }, cards)
    expect(at({ member: 'jev', presence: 'working', working: true }, ask)).toBe('working')
    expect(at({ member: 'jev', presence: 'offline' })).toBe('offline')
    // A newer daemon can send a state this client does not know.
    expect(at({ member: 'jev', presence: 'dreaming' })).toBe('idle')
  })

  it('works out the presence from an older daemon status that has none', () => {
    const ask: TalkootCard[] = [{ session: 's', member: 'jev', kind: 'ask', id: 'a' }]
    const at = (status: TalkootMember['status'], cards: TalkootCard[] = []) => presence({ id: 'jev', role: 'specialist', status }, cards)
    expect(at({ member: 'jev', working: true, paused: 'budget' }, ask)).toBe('paused')
    expect(at({ member: 'jev', working: true }, ask)).toBe('waiting')
    expect(at({ member: 'jev', working: true })).toBe('working')
    expect(at({ member: 'jev' })).toBe('idle')
  })

  it('filters a member conversation with the person', () => {
    const lines = [
      env('human:sothr', ['helm'], '2026-09-27T10:00:00Z'),
      env('helm', ['jev'], '2026-09-27T10:00:01Z'),
      env('helm', ['human:sothr'], '2026-09-27T10:00:02Z'),
      env('human:sothr', ['jev'], '2026-09-27T10:00:03Z'),
    ]
    expect(conversation(lines, 'helm').map((l) => l.at)).toEqual(['2026-09-27T10:00:00Z', '2026-09-27T10:00:02Z'])
    expect(unreadFor(lines, 'helm', '2026-09-27T10:00:00Z')).toBe(1)
    expect(unreadFor(lines, 'helm', '')).toBe(1)
  })

  it('scopes a member conversation to the person using the view', () => {
    const lines = [
      env('human:sothr', ['helm'], '2026-09-27T10:00:00Z'),
      env('human:aino', ['helm'], '2026-09-27T10:00:01Z'),
      env('helm', ['human:aino'], '2026-09-27T10:00:02Z'),
      env('helm', ['human:sothr'], '2026-09-27T10:00:03Z'),
    ]
    expect(conversation(lines, 'helm', 'sothr').map((l) => l.at)).toEqual(['2026-09-27T10:00:00Z', '2026-09-27T10:00:03Z'])
    expect(unreadFor(lines, 'helm', '', 'sothr')).toBe(1)
    // Without a name the view only watches, and shows every person.
    expect(conversation(lines, 'helm')).toHaveLength(4)
    expect(unreadFor(lines, 'helm', '')).toBe(2)
  })

  it('collapses a run between two members and hides bookkeeping lines', () => {
    const lines: TalkootLine[] = [
      env('helm', ['jev'], '2026-09-27T10:00:00Z'),
      { type: 'delivery', at: '2026-09-27T10:00:00Z', member: 'jev' },
      env('jev', ['helm'], '2026-09-27T10:00:01Z'),
      env('human:sothr', ['helm'], '2026-09-27T10:00:02Z'),
      env('helm', ['jev'], '2026-09-27T10:00:03Z'),
    ]
    const items = roomItems(lines)
    // The lone message after the person's line stands alone.
    expect(items.map((i) => i.kind)).toEqual(['exchange', 'line', 'line'])
    expect(items[0].kind === 'exchange' && items[0].lines).toHaveLength(2)
  })

  it('keeps an exchange whole across signal lines', () => {
    const lines: TalkootLine[] = [
      env('helm', ['jev'], '2026-09-27T10:00:00Z'),
      { type: 'tool_error', at: '2026-09-27T10:00:01Z', member: 'jev', tool: 'bash' },
      { type: 'retry', at: '2026-09-27T10:00:01Z', member: 'jev', attempt: 1 },
      { type: 'card_open', at: '2026-09-27T10:00:01Z', member: 'jev', card: 'question', ref: 'a1' },
      { type: 'card_close', at: '2026-09-27T10:00:01Z', member: 'jev', card: 'question', ref: 'a1', outcome: 'answered' },
      env('jev', ['helm'], '2026-09-27T10:00:02Z'),
    ]
    const items = roomItems(lines)
    expect(items.map((i) => i.kind)).toEqual(['exchange'])
    expect(items[0].kind === 'exchange' && items[0].lines).toHaveLength(2)
  })
})

describe('the talkoot hub', () => {
  it('hands talkoots_changed to the list listeners, whatever its address', () => {
    const hub = new TalkootHub()
    const list = vi.fn()
    const room = vi.fn()
    const stop = hub.onList(list)
    hub.listen('crew', room)
    expect(hub.dispatch('#workspace', { type: 'talkoots_changed' } as WireEvent)).toBe(true)
    expect(list).toHaveBeenCalledTimes(1)
    expect(room).not.toHaveBeenCalled()
    stop()
    hub.dispatch('#workspace', { type: 'talkoots_changed' } as WireEvent)
    expect(list).toHaveBeenCalledTimes(1)
  })

  it('hands a room address to its listeners and nothing else', () => {
    const hub = new TalkootHub()
    const fn = vi.fn()
    const stop = hub.listen('crew', fn)
    const e = ev('talkoot_status', { id: 'crew' })
    expect(hub.dispatch('#talkoot:crew', e)).toBe(true)
    expect(fn).toHaveBeenCalledWith(e)
    expect(hub.dispatch('#talkoot:other', e)).toBe(true)
    expect(hub.dispatch('session-1', e)).toBe(false)
    stop()
    hub.dispatch('#talkoot:crew', e)
    expect(fn).toHaveBeenCalledTimes(1)
  })
})
