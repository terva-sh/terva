import type { ComponentChildren } from 'preact'
import { useEffect, useMemo, useRef, useState } from 'preact/hooks'
import { t, tn } from '../../i18n'
import type { ClientLike } from '../../platform/ctrlproto/client'
import type {
  ServerHello,
  TalkootDraft,
  TalkootDraftsResult,
  TalkootListResult,
  TalkootSummary,
  TalkootTemplate,
  TalkootTemplatesResult,
} from '../../platform/ctrlproto/types'
import type { TalkootHub } from '../../platform/talkoot/hub'
import { AppSwitcher } from '../../ui/AppSwitcher'
import { NewTalkoot } from '../../ui/talkoot/NewTalkoot'
import { noticeSupport, useTalkootNotify, useTeamNotices } from '../../ui/talkoot/notify'
import { useTalkootPerson } from '../../ui/talkoot/person'
import { TeamManager } from '../../ui/talkoot/TeamManager'
import { PersonInput, useTeamTab } from '../../ui/talkoot/TalkootScreen'
import { TeamMark, teamStateLabel } from '../../ui/talkoot/TeamMark'
import { TeamView } from './TeamView'

// Shell holds what is true across teams: the talkoots on this daemon, the one
// open, the person's name, and whether the new-team form is open. TeamView
// draws one team. The rail's team list and the person's name come from here.
//
// link is a ?team= and ?member= deep link, already validated. Its team wins over
// the one this browser last opened, and its member opens only in that team.
export function Shell({
  client,
  hub,
  generation,
  workspace = '',
  hello,
  link = { team: '', member: '' },
}: {
  client: ClientLike
  hub: TalkootHub
  generation: number
  workspace?: string
  hello?: ServerHello | null
  link?: { team: string; member: string }
}) {
  const [list, setList] = useState<TalkootSummary[] | null>(null)
  const [error, setError] = useState('')
  const [picked, setPicked] = useState(() => link.team || (localStorage.getItem('terva_talkoot') ?? ''))
  // The link is spent the first time the person picks a team, so coming back
  // to the linked team later opens its room rather than the linked member.
  const [linked, setLinked] = useState(link)
  const [creating, setCreating] = useState(false)
  // What the first-run page opened the form on: a template, the builder, or a
  // team manager's draft.
  const [start, setStart] = useState<Start>({})
  // 🔑 Each open of the form remounts it. The form reads its start only on
  // mount, so a second Review while the builder is open would otherwise keep
  // the first draft's id and members.
  const [opened, setOpened] = useState(0)
  const create = (s: Start = {}) => {
    setStart(s)
    setOpened((n) => n + 1)
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
  const [drafts, setDrafts] = useState<TalkootDraft[]>([])
  const draftReads = useRef(0)
  // A daemon without the verb answers with an error, and the app shows no
  // drafts rather than a problem.
  const refreshDrafts = () => {
    const mine = ++draftReads.current
    client.send<TalkootDraftsResult>('talkoot.drafts', null).then(
      (r) => {
        if (mine === draftReads.current) setDrafts(r.drafts ?? [])
      },
      () => {},
    )
  }
  const discard = (id: string) => {
    client.send('talkoot.discard_draft', { id }).then(
      () => refreshDrafts(),
      (e: unknown) => setError(e instanceof Error ? e.message : String(e)),
    )
  }
  const refresh = () => {
    const mine = ++reads.current
    refreshDrafts()
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
    setLinked({ team: '', member: '' })
    setPicked(id)
    localStorage.setItem('terva_talkoot', id)
    setCreating(false)
    setDrawer(false)
  }

  // The app switcher heads the rail, above the teams, wherever the rail is
  // drawn: here, and in TeamView.
  const teams = (
    <>
      <div class="tka-apps">
        <AppSwitcher hello={hello} current="talkoot" />
      </div>
      <TeamList
        teams={running}
        broken={(list ?? []).filter((x) => !x.running && x.problem)}
        current={creating ? '' : current}
        onPick={pick}
        onNew={() => create()}
        // The first-run page shows the drafts itself, so the rail does not
        // list them twice.
        drafts={!creating && !team ? null : <DraftList drafts={drafts} onReview={(d) => create({ draft: d })} onDiscard={discard} />}
      />
    </>
  )
  const you = (
    <div class="tka-you">
      <span class="talkoot-note">{t('You are')}</span> <PersonInput person={person} onCommit={setPerson} />
      <TeamManager client={client} />
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
        initialMember={current === linked.team ? linked.member : ''}
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
            key={opened}
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
            initialDraft={start.draft}
          />
        ) : (
          list && (
            <FirstRun
              client={client}
              workspace={workspace}
              person={person}
              onPerson={setPerson}
              onCreate={create}
              drafts={<DraftList drafts={drafts} onReview={(d) => create({ draft: d })} onDiscard={discard} />}
            />
          )
        )}
      </main>
    </div>
  )
}

// Start is what the new-team form opens on.
type Start = { template?: string; build?: boolean; draft?: { id: string; text: string } }

// DraftList shows the team manager's drafts of new teams. Review opens the
// builder on one, and only the person's Create there makes the team.
function DraftList({
  drafts,
  onReview,
  onDiscard,
}: {
  drafts: TalkootDraft[]
  onReview: (d: TalkootDraft) => void
  onDiscard: (id: string) => void
}) {
  if (drafts.length === 0) return null
  return (
    <section class="talkoot-drafts" aria-label={t('Drafts from the team manager')}>
      <h3 class="tka-head">{t('Drafts from the team manager')}</h3>
      {drafts.map((d) => (
        <div key={d.id} class="talkoot-draft">
          <strong>{d.id}</strong>
          {d.problem ? <p class="talkoot-error">{t('This draft cannot be read: %s', d.problem)}</p> : <p class="talkoot-note">{d.why}</p>}
          <div class="card-actions">
            <button class="btn sm" disabled={!!d.problem} onClick={() => onReview(d)}>
              {t('Review')}
            </button>
            <button class="btn sm ghost" onClick={() => onDiscard(d.id)}>
              {t('Discard')}
            </button>
          </div>
        </div>
      ))}
    </section>
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
  drafts,
}: {
  client: ClientLike
  workspace: string
  person: string
  onPerson: (name: string) => void
  onCreate: (s?: Start) => void
  drafts: ComponentChildren
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
      {drafts}
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
      <p>
        <TeamManager client={client} primary />{' '}
        <span class="talkoot-note">{t('Or describe the team you want to the team manager. It drafts a team for you to review and create.')}</span>
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
  drafts,
}: {
  teams: TalkootSummary[]
  broken: TalkootSummary[]
  current: string
  onPick: (id: string) => void
  onNew: () => void
  drafts: ComponentChildren
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
      {drafts}
    </section>
  )
}
