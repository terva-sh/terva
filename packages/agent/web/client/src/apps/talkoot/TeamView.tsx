import type { ComponentChildren } from 'preact'
import { useCallback, useEffect, useMemo, useState } from 'preact/hooks'
import { t, tn } from '../../i18n'
import type { ClientLike } from '../../platform/ctrlproto/client'
import type { TalkootCard, TalkootRefText, TalkootSummary } from '../../platform/ctrlproto/types'
import type { TalkootHub } from '../../platform/talkoot/hub'
import { cardKey, compareTime, conversation, coordinator, lineKey, presence, threadItems, unreadFor } from '../../platform/talkoot/store'
import { usePinnedTail } from '../../ui/pinnedtail'
import { faceFor, sideIn } from '../../ui/talkoot/face/adapter'
import { MarksContext, MemberMark } from '../../ui/talkoot/MemberMark'
import { memberModelSummary } from '../../ui/talkoot/memberModel'
import { validPerson } from '../../ui/talkoot/person'
import { OpenRefContext, ShowRefContext } from '../../ui/talkoot/RefChip'
import { RoomLine } from '../../ui/talkoot/RoomLine'
import { InboxCard } from '../../ui/talkoot/TalkootInbox'
import { TalkootComposer } from '../../ui/talkoot/TalkootComposer'
import { PersonInput, TeamColor } from '../../ui/talkoot/TalkootScreen'
import { loadSeen, MotionPicker, orderMembers, PRESENCE_LABEL, saveSeen } from '../../ui/talkoot/TalkootTeam'
import { TeamBudget } from '../../ui/talkoot/TeamBudget'
import { SaveTemplate } from '../../ui/talkoot/SaveTemplate'
import { useTalkoot } from '../../ui/talkoot/useTalkoot'
import { SidePanel, type SideTab } from './SidePanel'
import { Thread } from './Thread'

// ROOM selects the room rather than a member's thread.
const ROOM = '#room'

// TeamView is one talkoot in the app's three columns: the rail with the teams,
// this team, and its members; the conversation, which is the room or one
// member's thread with the person; and the composer under it. Cards that wait
// for a person sit in the conversation at the time they opened. The side panel
// on the right holds the member's card and work, the team's notes, and the file
// a reference names.
export function TeamView({
  client,
  hub,
  team,
  generation,
  person,
  onPerson,
  teams,
  you,
  listError,
  onTeamChanged,
}: {
  client: ClientLike
  hub: TalkootHub
  team: TalkootSummary
  generation: number
  person: string
  onPerson?: (name: string) => void
  teams: ComponentChildren
  you: ComponentChildren
  listError: string
  onTeamChanged: () => void
}) {
  const id = team.id
  const { state, error, loadOlder, refreshView } = useTalkoot(client, hub, id, generation)
  const [selected, setSelected] = useState(ROOM)
  const [actionError, setActionError] = useState('')
  const [drawer, setDrawer] = useState(false)
  // A jump to a card waits for the view that holds it to render.
  const [jumpTo, setJumpTo] = useState('')
  const [openThreads, setOpenThreads] = useState<Set<string>>(() => new Set())
  const [side, setSide] = useState<SideTab | null>(null)
  const [fileRef, setFileRef] = useState('')

  const view = state.view
  const lead = coordinator(view)
  const members = view?.members ?? []
  const current = selected === ROOM || members.some((m) => m.id === selected) ? selected : ROOM
  const member = members.find((m) => m.id === current)
  const canSteer = validPerson(person)
  const me = canSteer ? person : ''

  // What the person has read in each member's thread, kept per person, so two
  // people who share a browser keep their own counts.
  const seenKey = `terva_talkoot_seen:${id}:${me}`
  const [seenFor, setSeenFor] = useState(() => ({ key: seenKey, seen: loadSeen(seenKey) }))
  if (seenFor.key !== seenKey) setSeenFor({ key: seenKey, seen: loadSeen(seenKey) })
  const seen = seenFor.key === seenKey ? seenFor.seen : {}

  const lines = useMemo(() => (current === ROOM ? state.lines : conversation(state.lines, current, me)), [state.lines, current, me])
  const cards = useMemo(() => (current === ROOM ? state.cards : state.cards.filter((c) => c.member === current)), [state.cards, current])
  const items = useMemo(() => threadItems(lines, cards), [lines, cards])
  const tail = usePinnedTail<HTMLDivElement>([items], current)

  useEffect(() => {
    if (current === ROOM || lines.length === 0) return
    const at = lines[lines.length - 1].at
    if (seenFor.key !== seenKey || seen[current] === at) return
    const next = { ...seen, [current]: at }
    setSeenFor({ key: seenKey, seen: next })
    saveSeen(seenKey, next)
  }, [current, lines, seenFor, seenKey])

  useEffect(() => {
    if (!jumpTo) return
    const el = document.querySelector(`[data-card="${CSS.escape(jumpTo)}"]`)
    if (!el) return
    el.scrollIntoView({ block: 'center', behavior: 'smooth' })
    setJumpTo('')
  }, [jumpTo, items])

  const marks = useMemo(() => Object.fromEntries(members.flatMap((m) => (m.mark ? [[m.id, m.mark]] : []))), [members])
  const openRef = useCallback((ref: string) => client.send<TalkootRefText>('talkoot.ref', { id, ref }), [client, id])
  const showRef = useCallback((ref: string) => {
    setFileRef(ref)
    setSide('file')
  }, [])
  const act = (p: Promise<unknown>) =>
    p.then(
      () => setActionError(''),
      (e: unknown) => setActionError(e instanceof Error ? e.message : String(e)),
    )
  const pause = (target: { member?: string; chain?: string }) =>
    act(client.send('talkoot.pause', { id, by: person, ...target, reason: t('paused from the Talkoot app') }))
  const resume = (target: { member?: string; chain?: string }) => act(client.send('talkoot.resume', { id, by: person, ...target }))

  const roster = orderMembers(members, lead?.id)
  const rosterIds = roster.map((m) => m.id)
  const teamPaused = members.length > 0 && members.every((m) => m.status?.paused)
  const spend = members.reduce((n, m) => n + (m.status?.spend_usd ?? 0), 0)
  const oldestCard = oldest(state.cards)
  const choose = (sel: string) => {
    setSelected(sel)
    setDrawer(false)
  }

  return (
    <MarksContext.Provider value={marks}>
      <OpenRefContext.Provider value={openRef}>
        <ShowRefContext.Provider value={showRef}>
          <div class={`tka${drawer ? ' drawer-open' : ''}${side ? ' side-open' : ''}`}>
            <nav class="tka-rail" aria-label={t('Teams and members')}>
              {teams}
              <section class="tka-section tka-team" aria-label={t('This team')}>
                {team.color && (
                  <TeamColor
                    team={team}
                    person={person}
                    onChange={(color) => act(client.send('talkoot.update', { id, by: person, color }).then(onTeamChanged))}
                  />
                )}
                <button class={`tka-item${current === ROOM ? ' on' : ''}`} aria-current={current === ROOM} onClick={() => choose(ROOM)}>
                  <span class="tka-item-name"># {t('room')}</span>
                  {state.cards.length > 0 && <span class="talkoot-unread">{state.cards.length}</span>}
                </button>
              </section>
              <section class="tka-section" aria-label={t('Members')}>
                <h2 class="tka-head">{t('Members')}</h2>
                {roster.map((m) => {
                  const p = presence(m, state.cards)
                  const unread = m.id === current ? 0 : unreadFor(state.lines, m.id, seen[m.id] ?? '', me)
                  return (
                    <div key={m.id} class={`tka-member presence-${p}${current === m.id ? ' on' : ''}`}>
                      <button class="tka-item" aria-current={current === m.id} onClick={() => choose(m.id)}>
                        <MemberMark mark={m.mark} size={24} face={faceFor(m.status, p, state.beats[m.id], (to) => sideIn(rosterIds, m.id, to))} />
                        <span class="tka-member-text">
                          <span class="tka-item-name">
                            {m.title || m.id}
                            {m.id === lead?.id && <span class="talkoot-note"> · {t('coordinator')}</span>}
                          </span>
                          <span class="talkoot-member-model" title={memberModelSummary(m)}>
                            {memberModelSummary(m)}
                          </span>
                          <span class={`talkoot-member-activity presence-${p}`}>
                            {p === 'working' && m.status?.tool ? t('running %s', m.status.tool) : PRESENCE_LABEL[p]()}
                            {m.status?.paused ? `: ${m.status.paused}` : ''}
                          </span>
                        </span>
                        {unread > 0 && <span class="talkoot-unread">{unread}</span>}
                      </button>
                      {canSteer && (
                        <button
                          class="btn sm ghost"
                          title={m.status?.paused ? t('Resume this member') : t('Pause this member')}
                          aria-label={m.status?.paused ? t('Resume %s', m.id) : t('Pause %s', m.id)}
                          onClick={() => (m.status?.paused ? resume({ member: m.id }) : pause({ member: m.id }))}
                        >
                          {m.status?.paused ? '▶' : '⏸'}
                        </button>
                      )}
                    </div>
                  )
                })}
              </section>
              <section class="tka-section tka-foot">
                <span class="talkoot-spend">{t('Today: $%s', spend.toFixed(2))}</span>
                {view && <TeamBudget client={client} view={view} person={person} canSteer={canSteer} onError={setActionError} />}
                <SaveTemplate client={client} id={id} onError={setActionError} />
                {canSteer && (
                  <button class="btn sm" onClick={() => (teamPaused ? resume({}) : pause({}))}>
                    {teamPaused ? t('Resume the team') : t('Pause the team')}
                  </button>
                )}
                <MotionPicker />
              </section>
              {you}
            </nav>
            <button class="tka-scrim" aria-label={t('Close the menu')} onClick={() => setDrawer(false)} />
            <main class="tka-main">
              <header class="tka-main-head">
                <button class="btn sm ghost tka-menu" aria-label={t('Teams and members')} onClick={() => setDrawer(true)}>
                  ☰
                </button>
                <strong>{current === ROOM ? `# ${t('room')}` : member?.title || current}</strong>
                {member && (
                  <span class="talkoot-main-meta">
                    {[member.role, member.persona, member.driver, member.posture, memberModelSummary(member)].filter(Boolean).join(' · ')}
                  </span>
                )}
                <button
                  class="btn sm ghost tka-details"
                  aria-expanded={side !== null}
                  onClick={() => setSide(side ? null : member ? 'member' : 'notes')}
                >
                  {side ? t('Hide the details') : t('Details')}
                </button>
              </header>
              {(listError || error || actionError) && <div class="talkoot-error">{listError || error || actionError}</div>}
              {/* The name is asked for in place, before the first action that
                  needs it, rather than as a refusal after one. */}
              {!canSteer && (
                <label class="talkoot-field tka-banner tka-ask-name">
                  <span>{t('Your name in the room')}</span>
                  {onPerson ? <PersonInput person={person} onCommit={onPerson} /> : null}
                  <span class="talkoot-note">{t('You need one to post, answer, and pause.')}</span>
                </label>
              )}
              <div class="talkoot-log tka-log" role="log" ref={tail.ref} onScroll={tail.onScroll}>
                {state.next !== undefined && (
                  <button class="btn sm ghost talkoot-older" onClick={() => void loadOlder()}>
                    {t('Load earlier messages')}
                  </button>
                )}
                {items.map((it) =>
                  it.kind === 'card' ? (
                    <div key={`card:${cardKey(it.card)}`} data-card={cardKey(it.card)} class="tka-card">
                      <InboxCard client={client} id={id} card={it.card} person={person} canSteer={canSteer} onError={setActionError} />
                    </div>
                  ) : it.kind === 'thread' ? (
                    <Thread
                      key={`thread:${it.root}`}
                      members={it.members}
                      lines={it.lines}
                      open={openThreads.has(it.root)}
                      onToggle={(open) =>
                        setOpenThreads((prev) => {
                          const next = new Set(prev)
                          if (open) next.add(it.root)
                          else next.delete(it.root)
                          return next
                        })
                      }
                      onPauseChain={canSteer ? () => pause({ chain: it.root }) : undefined}
                    />
                  ) : (
                    <RoomLine
                      key={lineKey(it.line)}
                      line={it.line}
                      onPauseChain={canSteer && current === ROOM ? (c) => pause({ chain: c }) : undefined}
                      onResumeChain={canSteer && current === ROOM ? (c) => resume({ chain: c }) : undefined}
                    />
                  ),
                )}
                {items.length === 0 && state.paged && <div class="talkoot-note">{t('Nothing here yet.')}</div>}
              </div>
              {tail.showJump && (
                <button class="btn sm talkoot-jump" onClick={tail.jumpToLatest}>
                  {t('Jump to the newest message')}
                </button>
              )}
              {state.cards.length > 0 && oldestCard && (
                <div class="tka-strip" role="status">
                  <span>{tn(state.cards.length, '%d card waits for you', '%d cards wait for you')}</span>
                  <button
                    class="btn sm"
                    onClick={() => {
                      if (current !== ROOM && oldestCard.member !== current) setSelected(ROOM)
                      setJumpTo(cardKey(oldestCard))
                    }}
                  >
                    {t('Show the oldest')}
                  </button>
                </div>
              )}
              {canSteer && (
                // ⚠️ The key keeps the draft when a line above the composer appears.
                <TalkootComposer
                  key="composer"
                  complete
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
            </main>
            {side && (
              <SidePanel
                client={client}
                id={id}
                member={member}
                presence={member ? presence(member, state.cards) : 'idle'}
                person={person}
                canSteer={canSteer}
                tab={side}
                fileRef={fileRef}
                onTab={setSide}
                onClose={() => setSide(null)}
                onShowRef={showRef}
                onPause={() => member && pause({ member: member.id })}
                onResume={() => member && resume({ member: member.id })}
                onRefresh={() => void refreshView().catch((e: unknown) => setActionError(e instanceof Error ? e.message : String(e)))}
              />
            )}
          </div>
        </ShowRefContext.Provider>
      </OpenRefContext.Provider>
    </MarksContext.Provider>
  )
}

// oldest is the card that has waited longest. A card with no time sorts after
// every timed one, as it does in the conversation.
function oldest(cards: TalkootCard[]): TalkootCard | undefined {
  let o: TalkootCard | undefined
  for (const c of cards) {
    if (!o || (c.at && (!o.at || compareTime(c.at, o.at) < 0))) o = c
  }
  return o
}
