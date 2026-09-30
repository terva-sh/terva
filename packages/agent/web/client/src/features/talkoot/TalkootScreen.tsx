import { useEffect, useMemo, useRef, useState } from 'preact/hooks'
import { t } from '../../i18n'
import type { ClientLike } from '../../platform/ctrlproto/client'
import type { TalkootListResult, TalkootSummary } from '../../platform/ctrlproto/types'
import type { TalkootHub } from '../../platform/talkoot/hub'
import { MARK_PALETTE } from '../../platform/talkoot/marks'
import { TalkootTeam } from './TalkootTeam'
import { NewTalkoot } from './NewTalkoot'
import { useTalkootPerson, validPerson } from './person'
import { TeamMark, teamStateLabel } from './TeamMark'
import { teamIcon } from './face/team'

// TalkootScreen is the web client's one surface for watching a talkoot: a list
// of the talkoots on this host, and the team view of the one picked. Nothing
// else in terva renders a room (decided 2026-09-27).
export function TalkootScreen({
  client,
  hub,
  generation,
  onOpenSession,
  onClose,
}: {
  client: ClientLike
  hub: TalkootHub
  generation: number
  onOpenSession: (id: string) => void
  onClose: () => void
}) {
  const [list, setList] = useState<TalkootSummary[] | null>(null)
  const [error, setError] = useState('')
  const [picked, setPicked] = useState(() => localStorage.getItem('terva_talkoot') ?? '')
  const [creating, setCreating] = useState(false)
  const [person, setPerson] = useTalkootPerson()

  // Counts list reads. Only the newest may land: a read that started before
  // a create would otherwise replace the list with one that lacks the team.
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

  // The daemon says when the list changed: a team started, its roster
  // changed, or its state did. The list then reads again.
  useEffect(() => hub.onList(() => void refresh()), [client, hub])

  const running = useMemo(() => (list ?? []).filter((x) => x.running), [list])
  // Open the picked talkoot, or the first one running here.
  const current = running.find((x) => x.id === picked)?.id ?? running[0]?.id ?? ''
  const team = running.find((x) => x.id === current)
  useTeamTab(creating ? undefined : team)

  const pick = (id: string) => {
    setPicked(id)
    localStorage.setItem('terva_talkoot', id)
    setCreating(false)
  }

  return (
    <div class="talkoot">
      <header class="talkoot-bar">
        <nav class="talkoot-teams" aria-label={t('Teams')}>
          {running.length === 0 && <span class="talkoot-note">{t('No talkoot runs here')}</span>}
          {running.map((x) => (
            <button
              key={x.id}
              class={`talkoot-team-pick${x.id === current ? ' on' : ''}`}
              aria-pressed={x.id === current}
              onClick={() => pick(x.id)}
            >
              <TeamMark color={x.color} state={x.state} size={24} />
              <span class="talkoot-team-name">{x.title || x.name || x.id}</span>
              {teamStateLabel(x.state) && <span class={`talkoot-team-state state-${x.state}`}>{teamStateLabel(x.state)}</span>}
            </button>
          ))}
        </nav>
        {team?.color && !creating && (
          <TeamColor
            team={team}
            person={person}
            onChange={(color) =>
              client.send('talkoot.update', { id: team.id, by: person, color }).then(
                () => refresh(),
                (e: unknown) => setError(e instanceof Error ? e.message : String(e)),
              )
            }
          />
        )}
        <button class="btn sm" onClick={() => setCreating(true)}>
          {t('New team')}
        </button>
        <span class="talkoot-person">
          {t('You are')}{' '}
          <PersonInput person={person} onCommit={setPerson} />
        </span>
        <button class="btn sm ghost" onClick={onClose} title={t('Back to the session view')}>
          ✕
        </button>
      </header>
      {error && <div class="talkoot-error">{error}</div>}
      {(list ?? []).filter((x) => !x.running && x.problem).map((x) => (
        <div key={x.id} class="talkoot-error">
          {x.id}: {x.problem}
        </div>
      ))}
      {creating ? (
        <NewTalkoot
          client={client}
          taken={(list ?? []).map((x) => x.id)}
          onCreated={(id) => {
            // Open the new team now. A list read that fails must not strand
            // the person on the form of a team that exists, so the team goes
            // into the list until the read replaces it.
            setList((l) => (l?.some((x) => x.id === id) ? l : [...(l ?? []), { id, running: true }]))
            pick(id)
            void refresh()
          }}
          onCancel={() => setCreating(false)}
        />
      ) : current ? (
        <TalkootTeam
          key={current}
          client={client}
          hub={hub}
          id={current}
          generation={generation}
          person={person}
          onOpenSession={onOpenSession}
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
    </div>
  )
}

// TeamColor picks the team mark's colour from the palette, or returns it to
// the default the talkoot id picks. A change is a roster change, so it needs a
// person's name, as every other edit of the team does.
function TeamColor({ team, person, onChange }: { team: TalkootSummary; person: string; onChange: (color: string) => void }) {
  const can = validPerson(person)
  return (
    <details class="talkoot-team-color">
      <summary title={can ? undefined : t('Set your name to change the team')}>{t('Team colour')}</summary>
      <div class="talkoot-picker" role="group" aria-label={t('Team colour')}>
        {MARK_PALETTE.map((c) => (
          <button
            key={c}
            class="talkoot-swatch"
            style={{ background: c }}
            disabled={!can}
            aria-label={c}
            aria-pressed={sameColor(team.color, c)}
            onClick={() => !sameColor(team.color, c) && onChange(c)}
          />
        ))}
        {/* Default is live only when the roster sets a colour to remove. */}
        <button class="btn sm ghost" disabled={!can || !team.own_color} onClick={() => onChange('')}>
          {t('Default')}
        </button>
      </div>
    </details>
  )
}

// sameColor compares two #RRGGBB values, in which hex case changes nothing.
// The daemon refuses a colour the team shows already on the same terms.
const sameColor = (a: string | undefined, b: string) => a?.toLowerCase() === b.toLowerCase()

// useTeamTab puts the open team in the browser tab: its name and state in the
// title, and its mark as the icon. Leaving the team puts terva's own back.
function useTeamTab(team: TalkootSummary | undefined) {
  const name = team ? team.title || team.name || team.id : ''
  useEffect(() => {
    if (!team?.color) return
    const title = document.title
    const links = [...document.querySelectorAll<HTMLLinkElement>('link[rel~="icon"]')]
    const saved = links.map((l) => ({ href: l.getAttribute('href'), type: l.getAttribute('type') }))
    const state = teamStateLabel(team.state)
    document.title = state ? `${name} · ${state}` : name
    const href = 'data:image/svg+xml,' + encodeURIComponent(teamIcon(team.color, team.state))
    for (const l of links) {
      l.setAttribute('href', href)
      l.setAttribute('type', 'image/svg+xml')
    }
    return () => {
      document.title = title
      links.forEach((l, i) => {
        const s = saved[i]
        if (s.href === null) l.removeAttribute('href')
        else l.setAttribute('href', s.href)
        if (s.type === null) l.removeAttribute('type')
        else l.setAttribute('type', s.type)
      })
    }
  }, [team?.id, team?.color, team?.state, name])
}

// PersonInput edits the person's name as a draft, and commits it on Enter or
// when the field loses focus. Committed on each key, every prefix of a name
// would act as a person and write read marks under its own key.
function PersonInput({ person, onCommit }: { person: string; onCommit: (name: string) => void }) {
  const [draft, setDraft] = useState(person)
  useEffect(() => setDraft(person), [person])
  const commit = () => {
    if (draft.trim() !== person) onCommit(draft)
  }
  return (
    <input
      class="talkoot-person-input"
      aria-label={t('Your name in the room')}
      placeholder={t('your name')}
      value={draft}
      onInput={(e) => setDraft((e.target as HTMLInputElement).value)}
      onBlur={commit}
      onKeyDown={(e) => {
        if (e.key === 'Enter') commit()
      }}
    />
  )
}
