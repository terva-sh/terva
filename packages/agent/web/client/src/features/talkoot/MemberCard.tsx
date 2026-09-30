import { useEffect, useRef, useState } from 'preact/hooks'
import { t } from '../../i18n'
import type { ClientLike } from '../../platform/ctrlproto/client'
import type { PersonaView, TalkootMark, TalkootMember, TalkootOp, TalkootOpValue } from '../../platform/ctrlproto/types'
import { MARK_PALETTE, MARK_SHAPES } from '../../platform/talkoot/marks'
import { MemberMark } from './MemberMark'

// The postures a member may take: terva's approval modes.
const POSTURES = ['plan', 'ask', 'auto-edit', 'workspace', 'yolo']

// The fields the card edits as text, in the order it shows them, by class.
// Decision 0025 sorts every field into look, voice, or authority.
const VOICE = ['persona'] as const
const AUTHORITY = ['driver', 'model', 'tier', 'posture', 'workspace', 'tools', 'budget_usd_per_day', 'turns_per_day'] as const
type Field = 'title' | (typeof VOICE)[number] | (typeof AUTHORITY)[number]

const LABELS: Record<Field, () => string> = {
  title: () => t('Title'),
  persona: () => t('Persona'),
  driver: () => t('Driver'),
  model: () => t('Model'),
  tier: () => t('Tier'),
  posture: () => t('Posture'),
  workspace: () => t('Workspace'),
  tools: () => t('Tools'),
  budget_usd_per_day: () => t('Budget per day, USD'),
  turns_per_day: () => t('Turns per day'),
}

// shown is a field as the card's text box holds it.
function shown(m: TalkootMember, f: Field): string {
  const v = m[f]
  if (Array.isArray(v)) return v.join(', ')
  return v === undefined || v === null || v === 0 ? '' : String(v)
}

// valueOf turns a text box back into the value the roster takes. An empty box
// is null, which removes the field and returns the member to its default.
function valueOf(f: Field, s: string): TalkootOpValue {
  const v = s.trim()
  if (v === '') return null
  if (f === 'tools') return v.split(',').map((x) => x.trim()).filter(Boolean)
  // ⚠️ A number that does not parse goes as its text, so the daemon refuses
  // it. NaN would travel as null, and remove the field in silence.
  if (f === 'budget_usd_per_day' || f === 'turns_per_day') return Number.isFinite(Number(v)) ? Number(v) : v
  return v
}

// MemberCard is the one place to see and change a member: its look, its
// voice, and its authority, and the actions on it. A person's edit applies at
// once through talkoot.update, which records it as a roster line. It sends
// only the fields the person changed, so an edit made meanwhile to another
// field survives.
export function MemberCard({
  client,
  id,
  member,
  person,
  canSteer,
  onPause,
  onResume,
}: {
  client: ClientLike
  id: string
  member: TalkootMember
  person: string
  canSteer: boolean
  onPause: () => void
  onResume: () => void
}) {
  const [draft, setDraft] = useState<Partial<Record<Field, string>>>({})
  const [removing, setRemoving] = useState(false)
  const [voice, setVoice] = useState<PersonaView | null>(null)
  // The card keeps its own error, so a save does not clear an error the
  // inbox or the composer showed.
  const [error, setError] = useState('')
  // The mark the person last chose. A second pick merges into it rather than
  // into the member's mark, which may not have caught up with the first.
  // A refresh that lands while a pick is on its way may predate it, so the
  // member's mark replaces the choice only when no pick is in flight. Picks
  // go one after another, so an older pick never applies after a newer one.
  const chosen = useRef<TalkootMark | null>(member.own_mark ?? null)
  const inFlight = useRef(0)
  const marks = useRef<Promise<unknown>>(Promise.resolve())
  const own = useRef<TalkootMark | null>(null)
  own.current = member.own_mark ?? null
  const ownKey = JSON.stringify(member.own_mark ?? null)
  useEffect(() => {
    if (inFlight.current === 0) chosen.current = member.own_mark ?? null
  }, [ownKey])
  const [saving, setSaving] = useState(false)
  // 🔑 The team view keys the card by member, so another member gets a new
  // card, with no draft and no pick in flight. A roster change to this member
  // keeps the draft: an edit to one field made elsewhere must not erase what
  // the person is typing in another, and a box they did not touch shows the
  // new value anyway.

  // The persona's charter and the work it suits. The library is optional, so
  // a daemon that serves none shows the name alone. A member with no persona
  // runs the daemon's default, which the client cannot name.
  useEffect(() => {
    let live = true
    setVoice(null)
    if (!member.persona) return
    client.send<PersonaView>('personas.get', { ref: member.persona }).then(
      (p) => live && setVoice(p),
      () => {},
    )
    return () => {
      live = false
    }
  }, [client, member.persona])

  // edit resolves true when the daemon took the change.
  const edit = (ops: TalkootOp[]) =>
    client.send('talkoot.update', { id, by: person, ops }).then(
      () => {
        setError('')
        return true
      },
      (e: unknown) => {
        setError(e instanceof Error ? e.message : String(e))
        return false
      },
    )
  // pick sets one part of the mark. A part the mark already holds is no
  // change, and the daemon would refuse it, so nothing is sent.
  const pick = (part: Partial<TalkootMark>) => {
    const base = chosen.current ?? {}
    const next = { ...base, ...part }
    if (next.shape === base.shape && next.color === base.color) return
    sendMark(next)
  }
  // A reset already on its way is not sent again: the daemon would refuse
  // the second as no change.
  const reset = () => chosen.current !== null && sendMark(null)
  const sendMark = (next: TalkootMark | null) => {
    chosen.current = next
    inFlight.current++
    marks.current = marks.current.then(() =>
      edit([{ op: 'look', member: member.id, set: { mark: next } }]).then((ok) => {
        inFlight.current--
        // After a refusal the choice falls back to the mark the roster holds,
        // once no pick is left in flight. An earlier pick may be refused too,
        // so it is no base to return to.
        if (!ok && inFlight.current === 0) chosen.current = own.current
      }),
    )
  }
  const color = member.mark?.color ?? MARK_PALETTE[0]

  // A field counts as changed when its value differs, not its text, so a
  // tools list typed as "read,grep" matches "read, grep".
  const same = (f: Field, s: string) => JSON.stringify(valueOf(f, s)) === JSON.stringify(valueOf(f, shown(member, f)))
  const changed = (Object.keys(draft) as Field[]).filter((f) => draft[f] !== undefined && !same(f, draft[f]!))
  const save = () => {
    const sent = Object.fromEntries(changed.map((f) => [f, draft[f]!])) as Partial<Record<Field, string>>
    const set = Object.fromEntries(changed.map((f) => [f, valueOf(f, sent[f]!)]))
    // A saved box leaves the draft, unless the person typed in it again while
    // the save was on its way. A refused save keeps the draft to fix. Save
    // waits for the answer, so a second click cannot send the edit again.
    setSaving(true)
    void edit([{ op: 'edit', member: member.id, set }]).then((ok) => {
      setSaving(false)
      if (!ok) return
      setDraft((d) => Object.fromEntries(Object.entries(d).filter(([f, v]) => sent[f as Field] !== v)))
    })
  }

  const field = (f: Field) => (
    <label key={f} class="talkoot-field">
      <span>{LABELS[f]()}</span>
      {f === 'posture' ? (
        <select disabled={!canSteer} value={draft[f] ?? shown(member, f)} onChange={(e) => setDraft({ ...draft, [f]: e.currentTarget.value })}>
          <option value="">{t('(default)')}</option>
          {/* A posture this client does not list still shows as itself. */}
          {[...POSTURES, ...(member.posture && !POSTURES.includes(member.posture) ? [member.posture] : [])].map((p) => (
            <option key={p} value={p}>
              {p}
            </option>
          ))}
        </select>
      ) : (
        <input
          disabled={!canSteer}
          value={draft[f] ?? shown(member, f)}
          onInput={(e) => setDraft({ ...draft, [f]: e.currentTarget.value })}
        />
      )}
    </label>
  )

  return (
    <section class="talkoot-member-card" aria-label={t('Member card')}>
      {error && <div class="talkoot-error">{error}</div>}
      <header class="talkoot-card-header">
        <MemberMark mark={member.mark} size={40} />
        <div>
          <strong>{member.title || member.id}</strong>
          <div class="talkoot-note">
            <code>{member.id}</code> · {member.role}
          </div>
        </div>
      </header>

      <fieldset class="talkoot-card-section class-look">
        <legend>{t('Look')}</legend>
        {field('title')}
        <div class="talkoot-field">
          <span>{t('Shape')}</span>
          <div class="talkoot-picker" role="group" aria-label={t('Shape')}>
            {MARK_SHAPES.map((s) => (
              <button
                key={s}
                class="talkoot-pick-mark"
                disabled={!canSteer}
                aria-label={s}
                aria-pressed={member.mark?.shape === s}
                onClick={() => pick({ shape: s })}
              >
                <MemberMark mark={{ shape: s, color }} size={20} />
              </button>
            ))}
          </div>
        </div>
        <div class="talkoot-field">
          <span>{t('Colour')}</span>
          <div class="talkoot-picker" role="group" aria-label={t('Colour')}>
            {MARK_PALETTE.map((c) => (
              <button
                key={c}
                class="talkoot-swatch"
                style={{ background: c }}
                disabled={!canSteer}
                aria-label={c}
                aria-pressed={member.mark?.color?.toUpperCase() === c}
                onClick={() => pick({ color: c })}
              />
            ))}
          </div>
        </div>
        {canSteer && member.own_mark && (
          <button class="btn sm" onClick={reset}>
            {t('Reset the mark to its default')}
          </button>
        )}
      </fieldset>

      <fieldset class="talkoot-card-section class-voice">
        <legend>{t('Instructions')}</legend>
        {VOICE.map(field)}
        {!member.persona && <p class="talkoot-note">{t('No persona set: the member runs the default persona.')}</p>}
        {voice?.good_for && voice.good_for.length > 0 && <p class="talkoot-note">{t('Good for: %s', voice.good_for.join(', '))}</p>}
        {voice?.avoid_for && voice.avoid_for.length > 0 && <p class="talkoot-note">{t('Avoid for: %s', voice.avoid_for.join(', '))}</p>}
        {voice?.charter && (
          <details>
            <summary>{t('The charter of %s', voice.name)}</summary>
            <pre class="preview">{voice.charter}</pre>
          </details>
        )}
      </fieldset>

      <fieldset class="talkoot-card-section class-authority">
        <legend>{t('Driver and limits')}</legend>
        <p class="talkoot-note">{t('These fields decide what the member can do.')}</p>
        {AUTHORITY.map(field)}
      </fieldset>

      {canSteer && (
        <div class="card-actions">
          <button class="btn primary" disabled={changed.length === 0 || saving} onClick={save}>
            {t('Save changes')}
          </button>
          <button class="btn" disabled={changed.length === 0} onClick={() => setDraft({})}>
            {t('Discard')}
          </button>
          <button class="btn" onClick={member.status?.paused ? onResume : onPause}>
            {member.status?.paused ? t('Resume') : t('Pause')}
          </button>
          {removing ? (
            <>
              <button
                class="btn danger"
                disabled={saving}
                onClick={() => {
                  // A refused remove disarms, so the person can cancel.
                  setSaving(true)
                  void edit([{ op: 'remove', member: member.id }]).then((ok) => {
                    setSaving(false)
                    if (!ok) setRemoving(false)
                  })
                }}
              >
                {t('Remove %s from the team', member.id)}
              </button>
              <button class="btn" onClick={() => setRemoving(false)}>
                {t('Cancel')}
              </button>
            </>
          ) : (
            <button class="btn" onClick={() => setRemoving(true)}>
              {t('Remove')}
            </button>
          )}
        </div>
      )}
    </section>
  )
}
