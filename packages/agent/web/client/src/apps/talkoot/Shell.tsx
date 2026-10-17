import { useEffect, useMemo, useRef, useState } from 'preact/hooks'
import { t, tn } from '../../i18n'
import type { ClientLike } from '../../platform/ctrlproto/client'
import type { TalkootListResult, TalkootSummary, TalkootTemplate, TalkootTemplatesResult } from '../../platform/ctrlproto/types'
import type { TalkootHub } from '../../platform/talkoot/hub'
import { panelHref } from '../../ui/navlinks'
import { NewTalkoot } from '../../ui/talkoot/NewTalkoot'
import { noticeSupport, useTalkootNotify, useTeamNotices } from '../../ui/talkoot/notify'
import { useTalkootPerson } from '../../ui/talkoot/person'
import { PersonInput, useTeamTab } from '../../ui/talkoot/TalkootScreen'
import { TeamMark, teamStateLabel } from '../../ui/talkoot/TeamMark'
import { TeamView } from './TeamView'

// Shell holds what is true across teams: the talkoots on this daemon, the one
// open, the person's name, and whether the new-team form is open. TeamView
// draws one team. The rail's team list and the person's name come from here.
export function Shell({ client, hub, generation, workspace = '' }: { client: ClientLike; hub: TalkootHub; generation: number; workspace?: string }) {
  const [list, setList] = useState<TalkootSummary[] | null>(null)
  const [error, setError] = useState('')
  const [picked, setPicked] = useState(() => localStorage.getItem('terva_talkoot') ?? '')
  const [creating, setCreating] = useState(false)
  // What the first-run page opened the form on: a template, or the builder.
  const [start, setStart] = useState<{ template?: string; build?: boolean }>({})
  const create = (s: { template?: string; build?: boolean } = {}) => {
    setStart(s)
    setCreating(true)
    setDrawer(false)
  }
  // The drawer is the rail on a narrow screen, here and in TeamView.
  const [drawer, setDrawer] = useState(false)
  const [person, setPerson] = useTalkootPerson()
  const [notify, setNotify, refused] = useTalkootNotify()

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
  // TeamView hears the open team's cards. This covers every other team.
  useTeamNotices(client, running, creating ? '' : current, notify)

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
      onNew={() => create()}
    />
  )
  const you = (
    <div class="tka-you">
      <span class="talkoot-note">{t('You are')}</span> <PersonInput person={person} onCommit={setPerson} />
      <a class="btn sm ghost" href={panelHref()} title={t('Open the control panel')}>
        {t('Panel')}
      </a>
      {noticeSupport() !== 'none' && (
        <label class="tka-notify talkoot-note">
          <input type="checkbox" checked={notify} onChange={(e) => void setNotify((e.target as HTMLInputElement).checked)} />{' '}
          {t('Notify me when a card waits')}
        </label>
      )}
      {refused && <span class="talkoot-note">{t('The browser refused notifications. Allow them for this site, then try again.')}</span>}
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
        onPerson={setPerson}
        teams={teams}
        you={you}
        listError={error}
        onTeamChanged={() => void refresh()}
        notify={notify}
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
            initialTemplate={start.template}
            initialBuild={start.build}
          />
        ) : (
          list && <FirstRun client={client} workspace={workspace} person={person} onPerson={setPerson} onCreate={create} />
        )}
      </main>
    </div>
  )
}

// FirstRun is the page a workspace with no team opens on. The person chose
// its parts on 2026-10-14 (TKT-01M4YJ5XF): what a talkoot is, the directory
// its teams belong to, the person's name, the templates, and the steps.
function FirstRun({
  client,
  workspace,
  person,
  onPerson,
  onCreate,
}: {
  client: ClientLike
  workspace: string
  person: string
  onPerson: (name: string) => void
  onCreate: (s?: { template?: string; build?: boolean }) => void
}) {
  const [templates, setTemplates] = useState<TalkootTemplate[]>([])
  useEffect(() => {
    client.send<TalkootTemplatesResult>('talkoot.templates', null).then(
      (r) => setTemplates((r.templates ?? []).filter((x) => !x.problem)),
      () => {},
    )
  }, [client])
  return (
    <div class="talkoot-empty talkoot-first-run">
      <h2>{t('Start your first team')}</h2>
      <p>
        {t('A talkoot is a team of agents that work together in one directory. You talk to its coordinator, it hands work to the other members, and each member asks you when a decision is yours.')}
      </p>
      <p class="talkoot-note">
        {workspace
          ? t('Teams here belong to %s. A team works in this directory, and only this daemon runs it.', workspace)
          : t('No talkoot runs in this workspace yet.')}
      </p>
      <label class="talkoot-field">
        <span>{t('Your name in the room')}</span>
        <PersonInput person={person} onCommit={onPerson} />
      </label>
      {!person && <p class="talkoot-note">{t('The team addresses you by this name. You need one to post, answer, and pause.')}</p>}
      <h3>{t('Pick a template')}</h3>
      <ul class="talkoot-template-list">
        {templates.map((x) => (
          <li key={x.name}>
            <button class="btn" onClick={() => onCreate({ template: x.name })}>
              {x.title || x.name}
            </button>{' '}
            <span class="talkoot-note">
              {x.description} {tn(x.members, '%d member.', '%d members.')}
            </span>
          </li>
        ))}
      </ul>
      <p>
        <button class="btn ghost" onClick={() => onCreate({ build: true })}>
          {t('Build a team member by member')}
        </button>
      </p>
      <h3>{t('What happens next')}</h3>
      <ol class="talkoot-steps">
        <li>{t('Name the team and set its daily budget.')}</li>
        <li>{t('Read the preview: every member, what it may do, and what it may spend.')}</li>
        <li>{t('Create the team. It waits for you on a Start the team card.')}</li>
        <li>{t('Run the introductions, or skip them.')}</li>
        <li>{t('Post to the room. The coordinator answers, or write @member to reach one member.')}</li>
      </ol>
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
