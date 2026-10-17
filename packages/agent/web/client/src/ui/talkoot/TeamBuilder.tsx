import { useEffect, useRef, useState } from 'preact/hooks'
import { t } from '../../i18n'
import type { ClientLike } from '../../platform/ctrlproto/client'
import type {
  PersonasListResult,
  TalkootPreview,
  TalkootTemplate,
  TalkootTemplatesResult,
  TalkootView,
} from '../../platform/ctrlproto/types'
import { composeRoster, draftFromPreview, emptyDraft, type DraftMember, type DraftTeam } from './builder'
import { POSTURES, TIERS } from './MemberCard'

const ID = /^[a-z][a-z0-9-]*$/
const ROLES = ['coordinator', 'planner', 'specialist']
const WORKSPACES = ['shared', 'worktree', 'either']
// A template preview needs an id. The builder's own name may still be empty,
// and the preview only reads the template's members.
const SEED_ID = 'draft'

// TeamBuilder composes a team on one screen. It starts empty or from any
// template, edits each member with the member card's fields, and holds the
// charter. The preview is the daemon's own check of the composed roster, with
// no template, and create writes exactly the text that preview returned.
export function TeamBuilder({
  client,
  taken,
  onCreated,
  onCancel,
  initialDraft,
}: {
  client: ClientLike
  taken: string[]
  onCreated: (id: string) => void
  onCancel: () => void
  // A team the team manager drafted. The builder opens on it, and the person
  // still previews and creates it.
  initialDraft?: { id: string; text: string }
}) {
  const [templates, setTemplates] = useState<TalkootTemplate[]>([])
  const [personas, setPersonas] = useState<string[]>([])
  const [id, setId] = useState(initialDraft?.id ?? '')
  // A draft shows no form until its preview lands, so no edit can race the
  // seed and drop the draft's members.
  const [seeding, setSeeding] = useState(!!initialDraft)
  const [team, setTeam] = useState<DraftTeam>(() => emptyDraft(''))
  const [seed, setSeed] = useState('')
  const [preview, setPreview] = useState<TalkootPreview | null>(null)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const seq = useRef(0)

  useEffect(() => {
    client.send<TalkootTemplatesResult>('talkoot.templates', null).then(
      (r) => setTemplates((r.templates ?? []).filter((x) => !x.problem)),
      () => {},
    )
    client.send<PersonasListResult>('personas.list', {}).then(
      // A daemon that serves no persona library answers with no list.
      (r) => setPersonas((r.personas ?? []).map((p) => p.ref)),
      () => {},
    )
  }, [client])

  // 🔑 A draft opens as the daemon reads it: the builder's own preview of the
  // manager's text fills the form, so the person edits the members the roster
  // really names. Nothing is created until the person previews and creates.
  useEffect(() => {
    if (!initialDraft) return
    client.send<TalkootPreview>('talkoot.preview', { text: initialDraft.text, id: initialDraft.id }).then(
      (pv) => {
        const d = draftFromPreview(pv, initialDraft.id)
        // A roster with no title reads its name as the title. Keep it empty.
        change(d.title === initialDraft.id ? { ...d, title: '' } : d)
        setSeeding(false)
      },
      (e: unknown) => {
        setError(e instanceof Error ? e.message : String(e))
        setSeeding(false)
      },
    )
  }, [client])

  // Any edit makes the preview stale, including one still in flight.
  const change = (next: DraftTeam) => {
    seq.current++
    setPreview(null)
    setBusy(false)
    setTeam(next)
  }
  const setMember = (i: number, patch: Partial<DraftMember>) =>
    change({ ...team, members: team.members.map((m, j) => (j === i ? { ...m, ...patch } : m)) })

  // The select shows a template only once its members are in the draft. An
  // edit while the seed loads drops the seed, and the select keeps the
  // start the draft really came from.
  const startFrom = (name: string) => {
    if (!name) {
      setSeed('')
      change({ ...emptyDraft(id) })
      return
    }
    const mine = ++seq.current
    setBusy(true)
    client.send<TalkootPreview>('talkoot.preview', { template: name, id: SEED_ID }).then(
      (pv) => {
        if (mine !== seq.current) return
        setBusy(false)
        setError('')
        setSeed(name)
        change(draftFromPreview(pv, id))
      },
      (e: unknown) => {
        if (mine !== seq.current) return
        setBusy(false)
        setError(e instanceof Error ? e.message : String(e))
      },
    )
  }

  const idProblem = !id ? '' : !ID.test(id) ? t('Use lower case letters, digits, and dashes, starting with a letter.') : taken.includes(id) ? t('A talkoot with this name exists.') : ''
  const ready = !!id && !idProblem && team.members.length > 0

  const runPreview = () => {
    const mine = ++seq.current
    setBusy(true)
    setPreview(null)
    // The roster's name is the team's id, as the template form writes it.
    const text = composeRoster({ ...team, name: id })
    client.send<TalkootPreview>('talkoot.preview', { text, id }).then(
      (pv) => {
        if (mine !== seq.current) return
        setPreview(pv)
        setError('')
        setBusy(false)
      },
      (e: unknown) => {
        if (mine !== seq.current) return
        setError(e instanceof Error ? e.message : String(e))
        setBusy(false)
      },
    )
  }

  const create = () => {
    if (!preview) return
    setBusy(true)
    // 🔑 Create sends the preview's text, the roster the daemon checked and
    // the person read, never a fresh compose of the form.
    client.send<TalkootView>('talkoot.create', { id: preview.id, text: preview.text }).then(
      (v) => onCreated(v.id),
      (e: unknown) => {
        setError(e instanceof Error ? e.message : String(e))
        setBusy(false)
      },
    )
  }

  const previewMembers = preview?.members ?? []
  const blocked = !preview || previewMembers.some((m) => !m.available) || (preview.problems?.length ?? 0) > 0

  if (seeding)
    return (
      <div class="talkoot-new talkoot-builder">
        <h2>{t('Build a team')}</h2>
        <p class="talkoot-note">{t('Loading the draft…')}</p>
      </div>
    )

  return (
    <div class="talkoot-new talkoot-builder">
      <h2>{t('Build a team')}</h2>
      {error && <div class="talkoot-error">{error}</div>}
      <label class="talkoot-field">
        <span>{t('Start from')}</span>
        <select value={seed} disabled={busy} onChange={(e) => startFrom((e.target as HTMLSelectElement).value)}>
          <option value="">{t('an empty team')}</option>
          {templates.map((x) => (
            <option key={x.name} value={x.name}>
              {x.title || x.name}
            </option>
          ))}
        </select>
      </label>
      <label class="talkoot-field">
        <span>{t('Name')}</span>
        <input value={id} placeholder={t('team')} onInput={(e) => { setId((e.target as HTMLInputElement).value.trim()); change(team) }} />
      </label>
      {idProblem && <p class="talkoot-error">{idProblem}</p>}
      <label class="talkoot-field">
        <span>{t('Title')}</span>
        <input value={team.title} onInput={(e) => change({ ...team, title: (e.target as HTMLInputElement).value })} />
      </label>
      <label class="talkoot-field">
        <span>{t('Daily budget (USD)')}</span>
        <input value={team.budget} inputMode="decimal" onInput={(e) => change({ ...team, budget: (e.target as HTMLInputElement).value })} />
      </label>
      <p class="talkoot-note">
        {t('A team needs exactly one coordinator. Only one member may write to the shared checkout: give the others posture plan or a worktree. yolo needs a worktree, and reviewer is for a specialist.')}
      </p>

      <datalist id="talkoot-builder-personas">
        {personas.map((p) => <option key={p} value={p} />)}
      </datalist>
      {team.members.map((m, i) => (
        <MemberRow
          key={i}
          m={m}
          onChange={(patch) => setMember(i, patch)}
          onRemove={() => change({ ...team, members: team.members.filter((_, j) => j !== i) })}
        />
      ))}
      <div class="card-actions">
        <button
          class="btn sm"
          onClick={() => change({ ...team, members: [...team.members, { id: `member-${team.members.length + 1}`, role: 'specialist' }] })}
        >
          {t('Add a member')}
        </button>
      </div>

      <label class="talkoot-field talkoot-charter">
        <span>{t('Charter')}</span>
        <textarea
          rows={6}
          value={team.charter}
          placeholder={t('What every member reads: how the team works, who decides, and when to ask you.')}
          onInput={(e) => change({ ...team, charter: (e.target as HTMLTextAreaElement).value })}
        />
      </label>

      <div class="card-actions">
        <button class="btn" disabled={!ready || busy} onClick={runPreview}>
          {t('Preview')}
        </button>
        <button class="btn ghost" onClick={onCancel}>
          {t('Cancel')}
        </button>
      </div>
      {preview && (
        <div class="talkoot-preview">
          <p>{t('Runs in %s with a daily budget of $%s.', preview.home, String(preview.budget_usd_per_day))}</p>
          {preview.problems && preview.problems.length > 0 && (
            <ul class="talkoot-problems">
              {preview.problems.map((p) => (
                <li key={p}>{p}</li>
              ))}
            </ul>
          )}
          {previewMembers.filter((m) => !m.available).map((m) => (
            <p key={m.id} class="talkoot-error">
              {m.id}: {m.problem}
            </p>
          ))}
          <details>
            <summary>{t('The talkoot.md this writes')}</summary>
            <pre class="preview">{preview.text}</pre>
          </details>
          <div class="card-actions">
            <button class="btn primary" disabled={blocked || busy} onClick={create}>
              {t('Create the team')}
            </button>
            {blocked && <span class="talkoot-note">{t('Fix every problem, then preview again.')}</span>}
          </div>
        </div>
      )}
    </div>
  )
}

function parseNumber(s: string): number | undefined {
  const v = s.trim()
  // A number that does not parse stays NaN, and the composer sends it as
  // text, so the daemon refuses it rather than dropping the cap.
  return v === '' ? undefined : Number(v)
}

function parseList(s: string): string[] | undefined {
  const list = s.split(',').map((x) => x.trim()).filter(Boolean)
  return list.length ? list : undefined
}

// RawInput keeps the text the person typed, so a half-typed value such as
// "1." or "read, " stays in the box while the parsed value goes to the
// draft. A change from outside, such as a new template, replaces the text
// only when it parses to something else.
function RawInput<T>({
  value,
  parse,
  format,
  label,
  inputMode,
  onChange,
}: {
  value: T
  parse: (s: string) => T
  format: (v: T) => string
  label: string
  inputMode?: 'decimal'
  onChange: (v: T) => void
}) {
  const [raw, setRaw] = useState(() => format(value))
  const key = JSON.stringify(value ?? null)
  useEffect(() => {
    if (JSON.stringify(parse(raw) ?? null) !== key) setRaw(format(value))
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [key])
  return (
    <input
      value={raw}
      inputMode={inputMode}
      aria-label={label}
      onInput={(e) => {
        const s = (e.target as HTMLInputElement).value
        setRaw(s)
        onChange(parse(s))
      }}
    />
  )
}

// MemberRow edits one member with the fields the member card offers.
function MemberRow({ m, onChange, onRemove }: { m: DraftMember; onChange: (patch: Partial<DraftMember>) => void; onRemove: () => void }) {
  const text = (f: 'id' | 'title' | 'persona' | 'driver' | 'model' | 'idle_stop', label: string, list?: string) => (
    <label class="talkoot-field">
      <span>{label}</span>
      <input
        value={m[f] ?? ''}
        list={list}
        aria-label={`${m.id} ${label}`}
        // The box keeps what the person typed, spaces included. composeRoster
        // trims each value when it writes the roster.
        onInput={(e) => onChange({ [f]: (e.target as HTMLInputElement).value || undefined })}
      />
    </label>
  )
  const choice = (f: 'role' | 'tier' | 'posture' | 'workspace', label: string, options: string[], blank: boolean) => (
    <label class="talkoot-field">
      <span>{label}</span>
      <select aria-label={`${m.id} ${label}`} value={m[f] ?? ''} onChange={(e) => onChange({ [f]: (e.target as HTMLSelectElement).value || undefined })}>
        {blank && <option value="">{t('(default)')}</option>}
        {[...options, ...(m[f] && !options.includes(m[f]!) ? [m[f]!] : [])].map((o) => (
          <option key={o} value={o}>
            {o}
          </option>
        ))}
      </select>
    </label>
  )
  const num = (f: 'budget_usd_per_day' | 'turns_per_day', label: string) => (
    <label class="talkoot-field">
      <span>{label}</span>
      <RawInput
        value={m[f]}
        parse={parseNumber}
        format={(v) => (v === undefined ? '' : String(v))}
        inputMode="decimal"
        label={`${m.id} ${label}`}
        onChange={(v) => onChange({ [f]: v })}
      />
    </label>
  )
  return (
    <fieldset class="talkoot-card-section talkoot-builder-member">
      <legend>
        <code>{m.id || t('(no id)')}</code>
      </legend>
      {text('id', t('Id'))}
      {choice('role', t('Role'), ROLES, false)}
      {text('title', t('Title'))}
      {text('persona', t('Persona'), 'talkoot-builder-personas')}
      {text('driver', t('Driver'))}
      {text('model', t('Model'))}
      {choice('tier', t('Tier'), TIERS, true)}
      {choice('posture', t('Posture'), POSTURES, true)}
      {choice('workspace', t('Workspace'), WORKSPACES, true)}
      <label class="talkoot-field">
        <span>{t('Reviewer')}</span>
        <input type="checkbox" aria-label={`${m.id} ${t('Reviewer')}`} checked={!!m.reviewer} onChange={(e) => onChange({ reviewer: (e.target as HTMLInputElement).checked || undefined })} />
      </label>
      {num('budget_usd_per_day', t('Budget per day, USD'))}
      {num('turns_per_day', t('Turns per day'))}
      <label class="talkoot-field">
        <span>{t('Tools')}</span>
        <RawInput
          value={m.tools}
          parse={parseList}
          format={(v) => (v ?? []).join(', ')}
          label={`${m.id} ${t('Tools')}`}
          onChange={(tools) => onChange({ tools })}
        />
      </label>
      {text('idle_stop', t('Stop the worker when idle for'))}
      <div class="card-actions">
        <button class="btn sm ghost" onClick={onRemove}>
          {t('Remove %s', m.id || t('this member'))}
        </button>
      </div>
    </fieldset>
  )
}
