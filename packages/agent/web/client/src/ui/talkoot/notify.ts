import { useEffect, useRef, useState } from 'preact/hooks'
import type { ClientLike } from '../../platform/ctrlproto/client'
import type { TalkootCard, TalkootInboxResult, TalkootSummary, WireEvent } from '../../platform/ctrlproto/types'
import type { TalkootHub } from '../../platform/talkoot/hub'
import { cardKey } from '../../platform/talkoot/store'
import { t } from '../../i18n'

// The person's choice to hear about a waiting card while the window is not in
// front. Off until the person turns it on (TKT-01M4YJ46K).
const KEY = 'terva_talkoot_notify'

// tervaNotify is the desktop window's binding (packages/agent/desktop_mode.go).
// tuohi denies the web Notification API on every engine, so the window posts
// a native notification instead. A browser has no such binding.
type NativeNotify = (title: string, message: string) => Promise<unknown>

function native(): NativeNotify | undefined {
  const fn = (window as unknown as { tervaNotify?: unknown }).tervaNotify
  return typeof fn === 'function' ? (fn as NativeNotify) : undefined
}

// NoticeSupport is how this page can notify: through the desktop window, the
// browser's API, or not at all.
export type NoticeSupport = 'desktop' | 'browser' | 'none'

export function noticeSupport(): NoticeSupport {
  if (native()) return 'desktop'
  return typeof Notification === 'function' ? 'browser' : 'none'
}

// useTalkootNotify holds the choice. Turning it on in a browser asks for the
// permission first, and a refusal leaves it off. The second value reports the
// refusal, so the toggle can say why it stayed off.
export function useTalkootNotify(): [boolean, (on: boolean) => Promise<void>, boolean] {
  const [on, setOn] = useState(() => localStorage.getItem(KEY) === 'on')
  const [refused, setRefused] = useState(false)
  const set = async (next: boolean) => {
    if (next && noticeSupport() === 'browser' && Notification.permission !== 'granted') {
      const answer = await Notification.requestPermission().catch(() => 'denied' as NotificationPermission)
      if (answer !== 'granted') {
        setRefused(true)
        return
      }
    }
    setRefused(false)
    setOn(next)
    if (next) localStorage.setItem(KEY, 'on')
    else localStorage.removeItem(KEY)
  }
  return [on, set, refused]
}

// inFront reports whether the person can see the page now.
export function inFront(): boolean {
  return !document.hidden && document.hasFocus()
}

// noticeLine names who waits and on what. It never carries the card's body,
// because a notification can show on a locked screen.
export function noticeLine(card: TalkootCard): string {
  switch (card.kind) {
    case 'permission':
      return t('%s asks you to approve a tool call', card.member)
    case 'ask':
      return t('%s asks you a question', card.member)
    case 'proposal':
      return t('%s proposes a roster change', card.member)
    case 'kickoff':
      return t('The team waits for you to start the introductions')
  }
  return t('%s waits for you', card.member)
}

// useCardNotices posts one notification for each card that arrives on the
// team's address while `on` holds and the page is not in front. A card the
// team already shows, which an event can send again, posts nothing.
//
// 🔑 It listens to the live events, and never to the inbox read. A reload
// or a reconnect reads every waiting card again, and none of those is news.
export function useCardNotices(hub: TalkootHub, id: string, team: string, on: boolean, cards: TalkootCard[]): void {
  const known = useRef(new Set<string>())
  known.current = new Set(cards.map(cardKey))
  const name = useRef(team)
  name.current = team
  useEffect(() => {
    if (!on) return
    return hub.listen(id, (ev: WireEvent) => {
      const card = ev.type === 'talkoot_inbox' ? ev.talkoot?.card : undefined
      if (!card) return
      const key = cardKey(card)
      if (known.current.has(key)) return
      known.current.add(key)
      if (!inFront()) postNotice(name.current, noticeLine(card))
    })
  }, [hub, id, on])
}

// useTeamNotices covers the teams the person does not have open. The client
// subscribes only to the open team's address, so their cards never arrive
// here. Their state does: talkoots_changed makes the list read again, and a
// team that turns needs-you posts one notice. The notice names the member of
// the newest card in that team's inbox.
//
// A second card in a team that already waits changes no state, so it posts
// nothing. The first one already told the person to look at that team.
//
// 🔑 The states are recorded while the setting is off too, and the first
// list a team appears in posts nothing. A team that waited before the person
// turned the setting on, or before a reload, is not news.
export function useTeamNotices(client: ClientLike, teams: TalkootSummary[], open: string, on: boolean): void {
  const seen = useRef(new Map<string, string | undefined>())
  // 🚨 The inbox read takes a round trip. When it settles, the person may
  // have turned the setting off, come to the front, or opened that team, the
  // team may have stopped waiting, and the hook may be gone. Each of those
  // drops the notice, so `now` reads the latest values and not the ones the
  // effect closed over.
  const states = new Map(teams.map((x) => [x.id, x.state]))
  const now = useRef({ on, open, states, live: true })
  now.current = { on, open, states, live: true }
  useEffect(
    () => () => {
      now.current = { ...now.current, live: false }
    },
    [],
  )
  useEffect(() => {
    const prev = seen.current
    const next = new Map<string, string | undefined>()
    for (const team of teams) {
      next.set(team.id, team.state)
      const was = prev.get(team.id)
      const turned = prev.has(team.id) && was !== 'needs-you' && team.state === 'needs-you'
      if (!on || !turned || team.id === open || inFront()) continue
      const name = team.title || team.name || team.id
      const id = team.id
      const post = (line: string) => {
        const n = now.current
        if (n.live && n.on && n.open !== id && n.states.get(id) === 'needs-you' && !inFront()) postNotice(name, line)
      }
      client.send<TalkootInboxResult>('talkoot.inbox', { id }).then(
        (r) => {
          // An empty inbox means someone answered the card already.
          const card = newest(r?.cards ?? [])
          if (card) post(noticeLine(card))
        },
        () => post(t('A card waits for you')),
      )
    }
    seen.current = next
  }, [client, teams, open, on])
}

function newest(cards: TalkootCard[]): TalkootCard | undefined {
  let best: TalkootCard | undefined
  for (const c of cards) if (!best || (c.at ?? '') >= (best.at ?? '')) best = c
  return best
}

// postNotice shows one notification. A failure is dropped: the card still
// waits in the app, and the strip above the composer still shows it.
export function postNotice(title: string, message: string): void {
  const fn = native()
  if (fn) {
    fn(title, message).catch(() => {})
    return
  }
  if (typeof Notification !== 'function' || Notification.permission !== 'granted') return
  try {
    new Notification(title, { body: message })
  } catch {
    /* some browsers allow notifications only from a service worker */
  }
}
