import { useEffect, useMemo, useRef, useState } from 'preact/hooks'
import { t } from '../../i18n'
import type { ClientLike } from '../../platform/ctrlproto/client'
import type { TalkootListResult, TalkootSummary } from '../../platform/ctrlproto/types'
import type { TalkootHub } from '../../platform/talkoot/hub'
import { panelHref } from '../../ui/navlinks'
import { NewTalkoot } from '../../ui/talkoot/NewTalkoot'
import { useTalkootPerson } from '../../ui/talkoot/person'
import { PersonInput, useTeamTab } from '../../ui/talkoot/TalkootScreen'
import { TeamMark, teamStateLabel } from '../../ui/talkoot/TeamMark'
import { TeamView } from './TeamView'

// Shell holds what is true across teams: the talkoots on this daemon, the one
// open, the person's name, and whether the new-team form is open. TeamView
// draws one team. The rail's team list and the person's name come from here.
export function Shell({ client, hub, generation }: { client: ClientLike; hub: TalkootHub; generation: number }) {
  const [list, setList] = useState<TalkootSummary[] | null>(null)
  const [error, setError] = useState('')
  const [picked, setPicked] = useState(() => localStorage.getItem('terva_talkoot') ?? '')
  const [creating, setCreating] = useState(false)
  // The drawer is the rail on a narrow screen, here and in TeamView.
  const [drawer, setDrawer] = useState(false)
  const [person, setPerson] = useTalkootPerson()

  // Only the newest list read may land. One that started before a create
  // would otherwise replace the list with one that lacks the new team.
  const reads = useRef(0)
  const refresh = () => {
    const mine = ++reads.current
    return client.send<TalkootListResult>('talkoot.list', null).then(
      (r) => {
        if (mine !== reads.current) return
        setList(r.talkoots)
        setError('')
      },
      (e: unknown) => {
        if (mine !== reads.current) return
        setError(e instanceof Error ? e.message : String(e))
      },
    )
  }
  useEffect(() => {
    if (generation > 0) void refresh()
  }, [client, generation])
  useEffect(() => hub.onList(() => void refresh()), [client, hub])

  const running = useMemo(() => (list ?? []).filter((x) => x.running), [list])
  const current = running.find((x) => x.id === picked)?.id ?? running[0]?.id ?? ''
  const team = running.find((x) => x.id === current)
  useTeamTab(creating ? undefined : team)

  const pick = (id: string) => {
    setPicked(id)
    localStorage.setItem('terva_talkoot', id)
    setCreating(false)
    setDrawer(false)
  }

  const teams = (
    <TeamList
      teams={running}
      broken={(list ?? []).filter((x) => !x.running && x.problem)}
      current={creating ? '' : current}
      onPick={pick}
      onNew={() => {
        setCreating(true)
        setDrawer(false)
      }}
    />
  )
  const you = (
    <div class="tka-you">
      <span class="talkoot-note">{t('You are')}</span> <PersonInput person={person} onCommit={setPerson} />
      <a class="btn sm ghost" href={panelHref()} title={t('Open the control panel')}>
        {t('Panel')}
      </a>
    </div>
  )

  if (!creating && team) {
    return (
      <TeamView
        key={current}
        client={client}
        hub={hub}
        team={team}
        generation={generation}
        person={person}
        teams={teams}
        you={you}
        listError={error}
        onTeamChanged={() => void refresh()}
      />
    )
  }
  return (
    <div class={`tka${drawer ? ' drawer-open' : ''}`}>
      <nav class="tka-rail" aria-label={t('Teams')}>
        {teams}
        {you}
      </nav>
      <button class="tka-scrim" aria-label={t('Close the menu')} onClick={() => setDrawer(false)} />
      <main class="tka-main">
        <header class="tka-main-head">
          <button class="btn sm ghost tka-menu" aria-label={t('Teams')} onClick={() => setDrawer(true)}>
            ☰
          </button>
          <strong>{creating ? t('New team') : t('Teams')}</strong>
        </header>
        {error && <div class="talkoot-error">{error}</div>}
        {creating ? (
          <NewTalkoot
            client={client}
            taken={(list ?? []).map((x) => x.id)}
            onCreated={(id) => {
              // Open the new team now. A list read that fails must not strand
              // the person on the form of a team that exists.
              setList((l) => (l?.some((x) => x.id === id) ? l : [...(l ?? []), { id, running: true }]))
              pick(id)
              void refresh()
            }}
            onCancel={() => setCreating(false)}
          />
        ) : (
          list && (
            <div class="talkoot-empty">
              <p>{t('No talkoot runs in this workspace yet.')}</p>
              <button class="btn primary" onClick={() => setCreating(true)}>
                {t('Create a team from a template')}
              </button>
            </div>
          )
        )}
      </main>
    </div>
  )
}

// TeamList is the rail's list of teams: each running talkoot with its mark and
// state, each one that failed to load with its problem, and the new-team form.
function TeamList({
  teams,
  broken,
  current,
  onPick,
  onNew,
}: {
  teams: TalkootSummary[]
  broken: TalkootSummary[]
  current: string
  onPick: (id: string) => void
  onNew: () => void
}) {
  return (
    <section class="tka-section" aria-label={t('Teams')}>
      <h2 class="tka-head">{t('Teams')}</h2>
      {teams.map((x) => (
        <button key={x.id} class={`tka-item${x.id === current ? ' on' : ''}`} aria-current={x.id === current} onClick={() => onPick(x.id)}>
          <TeamMark color={x.color} state={x.state} size={20} />
          <span class="tka-item-name">{x.title || x.name || x.id}</span>
          {teamStateLabel(x.state) && <span class={`talkoot-team-state state-${x.state}`}>{teamStateLabel(x.state)}</span>}
        </button>
      ))}
      {broken.map((x) => (
        <div key={x.id} class="talkoot-error">
          {x.id}: {x.problem}
        </div>
      ))}
      <button class="btn sm" onClick={onNew}>
        {t('New team')}
      </button>
    </section>
  )
}
