import { useCallback, useEffect, useMemo, useState } from 'preact/hooks'
import { t } from '../../i18n'
import { usePinnedTail } from '../../ui/pinnedtail'
import type { ClientLike } from '../../platform/ctrlproto/client'
import type { TalkootLine, TalkootMember, TalkootRefText } from '../../platform/ctrlproto/types'
import type { TalkootHub } from '../../platform/talkoot/hub'
import { conversation, coordinator, lineKey, presence, roomItems, unreadFor, type Presence } from '../../platform/talkoot/store'
import { TalkootComposer } from './TalkootComposer'
import { TalkootInbox } from './TalkootInbox'
import { RoomLine, RoomExchange } from './RoomLine'
import { OpenRefContext } from './RefChip'
import { MarksContext, MemberMark } from './MemberMark'
import { MemberCard } from './MemberCard'
import { validPerson } from './person'
import { useTalkoot } from './useTalkoot'
import { isWorker, WorkerEvents } from './WorkerEvents'

// ROOM selects the room rather than a member's conversation.
const ROOM = '#room'

const PRESENCE_LABEL: Record<Presence, () => string> = {
  idle: () => t('idle'),
  working: () => t('working'),
  waiting: () => t('waiting for you'),
  paused: () => t('paused'),
}

// TalkootTeam is one talkoot: the member sidebar, the selected view (the room
// or one member's conversation with the person), the inbox, and the composer.
// A new talkoot opens on its coordinator's conversation, and the room is one
// step away (open question 9 of the proposal).
export function TalkootTeam({
  client,
  hub,
  id,
  generation,
  person,
  onOpenSession,
}: {
  client: ClientLike
  hub: TalkootHub
  id: string
  generation: number
  person: string
  onOpenSession: (id: string) => void
}) {
  const { state, error, loadOlder } = useTalkoot(client, hub, id, generation)
  const [selected, setSelected] = useState('')
  const [actionError, setActionError] = useState('')
  const [cardOpen, setCardOpen] = useState(false)
  const [eventsOpen, setEventsOpen] = useState(false)
  // What the person has read, per member. Two people can share a browser,
  // so the store is keyed by the person, and a change of name loads theirs.
  const seenKey = `terva_talkoot_seen:${id}:${validPerson(person) ? person : ''}`
  const [seenFor, setSeenFor] = useState(() => ({ key: seenKey, seen: loadSeen(seenKey) }))
  if (seenFor.key !== seenKey) setSeenFor({ key: seenKey, seen: loadSeen(seenKey) })
  const seen = seenFor.key === seenKey ? seenFor.seen : {}
  const view = state.view
  const lead = coordinator(view)
  const members = view?.members ?? []
  // A selected member that left the roster falls back to the coordinator, so
  // a post never goes to a member that is gone.
  const kept = selected === ROOM || members.some((m) => m.id === selected) ? selected : ''
  const current = kept || lead?.id || ROOM
  const member = members.find((m) => m.id === current)
  const canSteer = validPerson(person)
  // The person the member views belong to. Without a valid name the view
  // only watches, and shows every person's exchanges.
  const me = canSteer ? person : ''

  // The exchanges the person opened, by the keys of their lines. A run that
  // an older page extends gets a new first line and remounts, and any of its
  // lines still finds it here.
  const [openRuns, setOpenRuns] = useState<Set<string>>(() => new Set())
  const toggleRun = (run: TalkootLine[], open: boolean) =>
    setOpenRuns((prev) => {
      if (run.every((l) => prev.has(lineKey(l)) === open)) return prev
      const next = new Set(prev)
      for (const l of run) {
        if (open) next.add(lineKey(l))
        else next.delete(lineKey(l))
      }
      return next
    })

  const lines = useMemo(
    () => (current === ROOM ? state.lines : conversation(state.lines, current, me)),
    [state.lines, current, me],
  )
  // The log follows its newest line until the person scrolls up. Another
  // member's view starts at its end.
  const tail = usePinnedTail<HTMLDivElement>([lines, state.cards], current)

  // Opening a member's conversation marks it read up to its newest line.
  useEffect(() => {
    if (current === ROOM || lines.length === 0) return
    const at = lines[lines.length - 1].at
    if (seenFor.key !== seenKey || seen[current] === at) return
    const next = { ...seen, [current]: at }
    setSeenFor({ key: seenKey, seen: next })
    saveSeen(seenKey, next)
  }, [current, lines, seenFor, seenKey])

  const marks = useMemo(() => Object.fromEntries(members.flatMap((m) => (m.mark ? [[m.id, m.mark]] : []))), [members])
  const openRef = useCallback((ref: string) => client.send<TalkootRefText>('talkoot.ref', { id, ref }), [client, id])

  const act = (p: Promise<unknown>) =>
    p.then(
      () => setActionError(''),
      (e: unknown) => setActionError(e instanceof Error ? e.message : String(e)),
    )

  const pause = (target: { member?: string; chain?: string }) =>
    act(client.send('talkoot.pause', { id, by: person, ...target, reason: t('paused from the web view') }))
  const resume = (target: { member?: string; chain?: string }) =>
    act(client.send('talkoot.resume', { id, by: person, ...target }))

  const teamPaused = members.length > 0 && members.every((m) => m.status?.paused)
  const spend = members.reduce((n, m) => n + (m.status?.spend_usd ?? 0), 0)

  return (
    <MarksContext.Provider value={marks}>
      <div class="talkoot-team">
        <nav class="talkoot-side" aria-label={t('Members')}>
          <button class={`talkoot-member${current === ROOM ? ' on' : ''}`} onClick={() => setSelected(ROOM)}>
            <span class="talkoot-member-name">{t('Room')}</span>
            <span class="talkoot-member-activity">{t('every message, in order')}</span>
          </button>
          {orderMembers(members, lead?.id).map((m) => {
            const p = presence(m, state.cards)
            const unread = m.id === current ? 0 : unreadFor(state.lines, m.id, seen[m.id] ?? '', me)
            return (
              <div key={m.id} class={`talkoot-member-row${current === m.id ? ' on' : ''}`}>
                <button class="talkoot-member" onClick={() => setSelected(m.id)}>
                  <span class="talkoot-member-name">
                    {m.id === lead?.id && <span title={t('Pinned: the coordinator')}>📌 </span>}
                    <MemberMark mark={m.mark} />
                    {m.title || m.id}
                    {unread > 0 && <span class="talkoot-unread">{unread}</span>}
                  </span>
                  <span class={`talkoot-member-activity presence-${p}`}>
                    {p === 'working' && m.status?.tool ? t('running %s', m.status.tool) : PRESENCE_LABEL[p]()}
                    {m.status?.paused ? `: ${m.status.paused}` : ''}
                    {m.status?.idle && !m.status.working ? ` · ${t('worker stopped while idle')}` : ''}
                  </span>
                </button>
                {canSteer && (
                  <button
                    class="btn sm ghost"
                    title={m.status?.paused ? t('Resume this member') : t('Pause this member')}
                    onClick={() => (m.status?.paused ? resume({ member: m.id }) : pause({ member: m.id }))}
                  >
                    {m.status?.paused ? '▶' : '⏸'}
                  </button>
                )}
              </div>
            )
          })}
          <div class="talkoot-side-foot">
            <span class="talkoot-spend">{t('Today: $%s', spend.toFixed(2))}</span>
            {canSteer && (
              <button class="btn sm" onClick={() => (teamPaused ? resume({}) : pause({}))}>
                {teamPaused ? t('Resume the team') : t('Pause the team')}
              </button>
            )}
          </div>
        </nav>
        <section class="talkoot-main">
          <div class="talkoot-main-head">
            <strong>{current === ROOM ? t('Room') : member?.title || current}</strong>
            {member && (
              <span class="talkoot-main-meta">
                {[member.role, member.persona, member.driver, member.posture].filter(Boolean).join(' · ')}
              </span>
            )}
            {member && (
              <button class="btn sm ghost" aria-expanded={cardOpen} onClick={() => setCardOpen(!cardOpen)}>
                {cardOpen ? t('Hide the member card') : t('Member card')}
              </button>
            )}
            {member && isWorker(member) ? (
              <button class="btn sm ghost" aria-expanded={eventsOpen} onClick={() => setEventsOpen(!eventsOpen)}>
                {eventsOpen ? t('Hide the worker events') : t('Worker events')}
              </button>
            ) : (
              member?.session && (
                <button class="btn sm ghost" onClick={() => onOpenSession(member.session!)}>
                  {t('Open session')}
                </button>
              )
            )}
          </div>
          {(error || actionError) && <div class="talkoot-error">{error || actionError}</div>}
          {!canSteer && <div class="talkoot-note">{t('Set your name above to post, answer, and pause.')}</div>}
          {member && eventsOpen && isWorker(member) && <WorkerEvents key={member.id} client={client} id={id} member={member} />}
          {member && cardOpen && (
            <MemberCard
              key={member.id}
              client={client}
              id={id}
              member={member}
              person={person}
              canSteer={canSteer}
              onPause={() => pause({ member: member.id })}
              onResume={() => resume({ member: member.id })}
            />
          )}
          <TalkootInbox client={client} id={id} cards={state.cards} person={person} canSteer={canSteer} onError={setActionError} />
          <OpenRefContext.Provider value={openRef}>
            <div class="talkoot-log" role="log" ref={tail.ref} onScroll={tail.onScroll}>
              {state.next !== undefined && (
                <button class="btn sm ghost talkoot-older" onClick={() => void loadOlder()}>
                  {t('Load earlier messages')}
                </button>
              )}
              {current === ROOM
                ? roomItems(lines).map((item) =>
                    item.kind === 'line' ? (
                      <RoomLine key={lineKey(item.line)} line={item.line} onPauseChain={canSteer ? (c) => pause({ chain: c }) : undefined} onResumeChain={canSteer ? (c) => resume({ chain: c }) : undefined} />
                    ) : (
                      <RoomExchange
                        key={lineKey(item.lines[0])}
                        members={item.members}
                        lines={item.lines}
                        onPauseChain={canSteer ? (c) => pause({ chain: c }) : undefined}
                        open={item.lines.some((l) => openRuns.has(lineKey(l)))}
                        onToggle={(open) => toggleRun(item.lines, open)}
                      />
                    ),
                  )
                : lines.map((l) => <RoomLine key={lineKey(l)} line={l} />)}
              {lines.length === 0 && state.paged && <div class="talkoot-note">{t('Nothing here yet.')}</div>}
            </div>
          </OpenRefContext.Provider>
          {tail.showJump && (
            <button class="btn sm talkoot-jump" onClick={tail.jumpToLatest}>
              {t('Jump to the newest message')}
            </button>
          )}
          {canSteer && (
            // ⚠️ The key keeps the draft. Unkeyed, the composer remounted when
            // a sibling line above it appeared, and an unsent draft was lost.
            <TalkootComposer
              key="composer"
              members={members.map((m) => m.id)}
              to={current === ROOM ? '' : current}
              onPost={(body, to) =>
                // The post rejects on failure, so the composer keeps the draft.
                client.send('talkoot.post', { id, by: person, body, to }).then(
                  () => setActionError(''),
                  (e: unknown) => {
                    setActionError(e instanceof Error ? e.message : String(e))
                    throw e
                  },
                )
              }
            />
          )}
        </section>
      </div>
    </MarksContext.Provider>
  )
}

// orderMembers puts the coordinator first and keeps the roster order.
function orderMembers(members: TalkootMember[], lead?: string): TalkootMember[] {
  return [...members].sort((a, b) => (a.id === lead ? -1 : b.id === lead ? 1 : 0))
}

// The time of the last line the person saw in each member's view, kept per
// talkoot in this browser. It is a view preference, like pins.
function loadSeen(key: string): Record<string, string> {
  try {
    return JSON.parse(localStorage.getItem(key) ?? '{}') as Record<string, string>
  } catch {
    return {}
  }
}

function saveSeen(key: string, seen: Record<string, string>) {
  localStorage.setItem(key, JSON.stringify(seen))
}
