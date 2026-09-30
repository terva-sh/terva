import { useEffect, useMemo, useRef, useState } from 'preact/hooks'
import { t } from '../../i18n'
import type { ClientLike } from '../../platform/ctrlproto/client'
import type { TalkootListResult, TalkootSummary } from '../../platform/ctrlproto/types'
import type { TalkootHub } from '../../platform/talkoot/hub'
import { TalkootTeam } from './TalkootTeam'
import { NewTalkoot } from './NewTalkoot'
import { useTalkootPerson } from './person'

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

  const running = useMemo(() => (list ?? []).filter((x) => x.running), [list])
  // Open the picked talkoot, or the first one running here.
  const current = running.find((x) => x.id === picked)?.id ?? running[0]?.id ?? ''

  const pick = (id: string) => {
    setPicked(id)
    localStorage.setItem('terva_talkoot', id)
    setCreating(false)
  }

  return (
    <div class="talkoot">
      <header class="talkoot-bar">
        <select
          class="talkoot-pick"
          aria-label={t('Talkoot')}
          value={current}
          onChange={(e) => pick((e.target as HTMLSelectElement).value)}
        >
          {running.length === 0 && <option value="">{t('No talkoot runs here')}</option>}
          {running.map((x) => (
            <option key={x.id} value={x.id}>
              {x.id}
            </option>
          ))}
        </select>
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
