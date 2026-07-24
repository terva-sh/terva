import { useEffect, useState } from 'preact/hooks'
import type { ClientLike } from '../../platform/ctrlproto/client'
import type { CardSummary, CardView, CardLintFinding, CardLintResult, DefaultForResult, DoctorProposal, DoctorResult, DoctorDecision } from '../../platform/ctrlproto/types'
import { m, t, tr } from '../../i18n'
import { Hint } from './Hint'
import { ModelPick } from './ModelPick'

// The card fields the doctor may edit (mirrors the server's allow-list) — the
// scalar text fields, so a proposal's `after` is a whole new string value.
const DOCTOR_FIELDS = new Set<keyof EditForm>([
  'name',
  'description',
  'personality',
  'scenario',
  'first_mes',
  'mes_example',
  'system_prompt',
  'post_history_instructions',
  'creator_notes',
])

// m()-marked (a module-level table) and tr()-translated where rendered.
const FIELD_LABELS: Record<string, string> = {
  name: m('Name'),
  description: m('Description'),
  personality: m('Personality'),
  scenario: m('Scenario'),
  first_mes: m('First message'),
  mes_example: m('Example dialogue'),
  system_prompt: m('System prompt'),
  post_history_instructions: m('Post-history instructions'),
  creator_notes: m('Creator notes'),
}

// The editable slice of a CCv2 `data` object — every field the editor exposes.
// Anything else in the stored document (extensions, character_book, unknown
// keys) is held in rawDoc and round-tripped untouched on save.
interface EditForm {
  name: string
  creator: string
  character_version: string
  description: string
  personality: string
  scenario: string
  first_mes: string
  alternate_greetings: string[]
  mes_example: string
  system_prompt: string
  post_history_instructions: string
  creator_notes: string
  tags: string[]
}

type RawDoc = { spec?: string; spec_version?: string; data?: Record<string, unknown> }

const str = (v: unknown): string => (typeof v === 'string' ? v : '')
const strArr = (v: unknown): string[] => (Array.isArray(v) ? v.filter((x): x is string => typeof x === 'string') : [])

function formFromData(data: Record<string, unknown>): EditForm {
  return {
    name: str(data.name),
    creator: str(data.creator),
    character_version: str(data.character_version),
    description: str(data.description),
    personality: str(data.personality),
    scenario: str(data.scenario),
    first_mes: str(data.first_mes),
    alternate_greetings: strArr(data.alternate_greetings),
    mes_example: str(data.mes_example),
    system_prompt: str(data.system_prompt),
    post_history_instructions: str(data.post_history_instructions),
    creator_notes: str(data.creator_notes),
    tags: strArr(data.tags),
  }
}

// A labeled text input or textarea with an optional ⓘ hint (discoverability),
// mirroring CardSheet's read-only Field so edit and inspect read the same.
function EditField(props: {
  label: string
  value: string
  hint?: string
  area?: boolean
  rows?: number
  onInput: (v: string) => void
}) {
  return (
    <label class="stage-editfield">
      <span class="stage-editfield__label">
        {props.label}
        {props.hint && <Hint text={props.hint} />}
      </span>
      {props.area ? (
        <textarea
          class="stage-editfield__area"
          rows={props.rows ?? 3}
          value={props.value}
          onInput={(e) => props.onInput((e.target as HTMLTextAreaElement).value)}
        />
      ) : (
        <input class="stage-editfield__input" value={props.value} onInput={(e) => props.onInput((e.target as HTMLInputElement).value)} />
      )}
    </label>
  )
}

// CardEditor is the writable sibling of CardSheet (rough-edge S7.1): edit every
// CCv2 field of a stored card and save it back through cards.edit, which
// re-serializes and keeps the id + avatar. The deterministic lint rides along
// (cards.lint), refreshed on each save, so a fix — e.g. the malformed macro on a
// so-so import — visibly clears. Unknown fields (extensions, character_book)
// live only in rawDoc and round-trip untouched.
// onSaved means "a save landed — refresh your copy of this card". It does NOT
// mean the user is finished: it fires on every save, including the intermediate
// one "Save & ask again" performs so the doctor re-reads the improved card. A
// caller that closes the editor from onSaved destroys the consultation in
// progress. Closing is onClose, which Cancel and × already call.
// A null `card` is a character being CREATED: there is nothing stored to fetch,
// so the form starts blank and the first save imports it. Everything that reads
// the stored card — the lint, the doctor, the per-card default model — waits
// until there is one, rather than asking about an id that does not exist yet.
// onCreated hands the minted id back so the host can adopt it (its route, its
// title) and the next save edits instead of importing a second copy.
// cards.import takes the document as BYTES (Go decodes []byte from base64), so a
// character created here is serialized and imported exactly as a downloaded card
// would be — one code path into the store rather than a second, create-only verb
// that would have to keep pace with it.
function encodeCardDoc(doc: unknown): string {
  const json = new TextEncoder().encode(JSON.stringify(doc))
  let s = ''
  for (let i = 0; i < json.length; i += 0x8000) s += String.fromCharCode(...json.subarray(i, i + 0x8000))
  return btoa(s)
}

export function CardEditor(props: {
  client: ClientLike
  card: CardSummary | null
  onClose: () => void
  onSaved: () => void
  onCreated?: (id: string) => void
}) {
  const { client, card, onClose, onSaved, onCreated } = props
  // The id this editor is bound to. Empty until a new character's first save
  // mints one; from then on it is the card's, and save() switches from import
  // to edit.
  const [cardId, setCardId] = useState(card?.id ?? '')
  const [rawDoc, setRawDoc] = useState<RawDoc | null>(null)
  const [form, setForm] = useState<EditForm | null>(null)
  const [findings, setFindings] = useState<CardLintFinding[] | null>(null)
  const [saving, setSaving] = useState(false)
  const [savedAt, setSavedAt] = useState(false)
  const [error, setError] = useState('')
  const [proposals, setProposals] = useState<DoctorProposal[] | null>(null)
  const [doctorNote, setDoctorNote] = useState('')
  const [doctorRunning, setDoctorRunning] = useState(false)
  const [doctorError, setDoctorError] = useState('')
  const [applied, setApplied] = useState<Record<string, boolean>>({})
  const [declined, setDeclined] = useState<Record<string, string>>({})
  const [decliningId, setDecliningId] = useState<string | null>(null)
  const [reasonDraft, setReasonDraft] = useState('')
  // A per-generation model override for the doctor (Phase 7): empty = the card's
  // effective default (Card → Workspace), which the daemon resolves on its own
  // when this is blank. Seeded below from the card's own default when it has one,
  // so the picker shows what the doctor will actually run on rather than a bare
  // "Workspace model". Run the doctor on a stronger/cheaper model without changing
  // any session's model.
  const [ovProvider, setOvProvider] = useState('')
  const [ovModel, setOvModel] = useState('')
  // What the author wants out of this consultation, in their own words. It is
  // standing direction, not a one-shot: it rides every round — including each
  // "Save & ask again" — so editing it between rounds is how you change course,
  // and clearing it returns the doctor to its ordinary lint-led pass. Per-
  // proposal decline reasons answer a single proposal; this steers the lot.
  const [steer, setSteer] = useState('')

  const lint = (id = cardId) => {
    if (!id) return // nothing stored to lint yet
    client
      .send<CardLintResult>('cards.lint', { id })
      .then((r) => setFindings(r.findings ?? []))
      .catch(() => setFindings(null))
  }

  useEffect(() => {
    if (!card) {
      // A new character: no fetch, an empty document, and a blank form. The
      // spec wrapper is what cards.import expects to receive back on save.
      setRawDoc({ spec: 'chara_card_v2', spec_version: '2.0', data: {} })
      setForm(formFromData({}))
      return
    }
    client
      .send<CardView>('cards.get', { id: card.id })
      .then((v) => {
        const doc = (v.raw as RawDoc) ?? {}
        setRawDoc(doc)
        setForm(formFromData(doc.data ?? {}))
      })
      .catch((e: unknown) => setError(String(e)))
    lint(card.id)
    // Seed the doctor's model from the card's own default when it has one, so the
    // picker names what the doctor will run on. Unsupported (old daemon) or no
    // card default leaves the override empty — the picker's Default row, and the
    // daemon's own fallback, then apply.
    client
      .send<DefaultForResult>('models.default_for', { card: card.id })
      .then((d) => {
        if (d.source === 'card') {
          setOvProvider(d.provider)
          setOvModel(d.model)
        }
      })
      .catch(() => {})
  }, [card?.id])

  const set = <K extends keyof EditForm>(key: K, value: EditForm[K]) => {
    setForm((f) => (f ? { ...f, [key]: value } : f))
    setSavedAt(false)
  }

  const setGreeting = (i: number, v: string) => set('alternate_greetings', form!.alternate_greetings.map((g, j) => (j === i ? v : g)))
  const addGreeting = () => set('alternate_greetings', [...form!.alternate_greetings, ''])
  const removeGreeting = (i: number) => set('alternate_greetings', form!.alternate_greetings.filter((_, j) => j !== i))

  // Ask the card doctor (the Seppä persona) for structured per-field edits over
  // the current card + its lint. S7.2 runs the first pass; the decline-with-
  // reason negotiation lands in S7.3.
  const runDoctor = async () => {
    setDoctorRunning(true)
    setDoctorError('')
    try {
      const r = await client.send<DoctorResult>('cards.doctor', { id: cardId, steer, provider: ovProvider, model: ovModel })
      setProposals(r.proposals ?? [])
      setDoctorNote(r.note ?? '')
    } catch (e) {
      setDoctorError(String(e))
    } finally {
      setDoctorRunning(false)
    }
  }

  // Apply a proposal: stage its `after` into the editor field (saved with the
  // rest on "Save changes"). Only the known scalar text fields are targets.
  // Applying clears any prior decline of the same proposal.
  const applyProposal = (p: DoctorProposal) => {
    const key = p.field as keyof EditForm
    if (!DOCTOR_FIELDS.has(key)) return
    set(key, p.after as never)
    setApplied((a) => ({ ...a, [p.id]: true }))
    setDeclined(({ [p.id]: _drop, ...rest }) => rest)
    if (decliningId === p.id) setDecliningId(null)
  }

  // Apply every still-pending proposal at once. Coordinated edits (e.g. the
  // doctor moving text from one field into another across two proposals) only
  // hold together when applied as a set — this stages the whole batch.
  const applyAll = () => {
    ;(proposals ?? []).forEach((p) => {
      if (!applied[p.id] && declined[p.id] === undefined) applyProposal(p)
    })
  }

  // Decline a proposal with a reason the doctor will weigh on the next pass.
  const confirmDecline = (id: string) => {
    setDeclined((d) => ({ ...d, [id]: reasonDraft.trim() }))
    setApplied(({ [id]: _drop, ...rest }) => rest)
    setDecliningId(null)
    setReasonDraft('')
  }

  // The user's verdicts on the current proposals, fed back so the doctor can
  // revise: applied → accepted, declined → the reason.
  const buildDecisions = (): DoctorDecision[] => {
    const out: DoctorDecision[] = []
    for (const p of proposals ?? []) {
      if (declined[p.id] !== undefined) out.push({ proposal_id: p.id, field: p.field, rationale: p.rationale, accepted: false, reason: declined[p.id] })
      else if (applied[p.id]) out.push({ proposal_id: p.id, field: p.field, rationale: p.rationale, accepted: true })
    }
    return out
  }

  // "Save & ask again": persist the staged edits (so the doctor re-reads the
  // IMPROVED card and re-lints it — a self-correcting loop) then re-run the pass
  // with the decline reasons, so the doctor revises, withdraws, or adds. The
  // verb already accepts decisions (built in S7.2).
  const reviseDoctor = async () => {
    setDoctorRunning(true)
    setDoctorError('')
    try {
      const decisions = buildDecisions()
      if (!(await save())) return // a failed save surfaces its own error; don't consult on a stale card
      const r = await client.send<DoctorResult>('cards.doctor', { id: cardId, decisions, steer, provider: ovProvider, model: ovModel })
      setProposals(r.proposals ?? [])
      setDoctorNote(r.note ?? '')
      setApplied({})
      setDeclined({})
      setDecliningId(null)
    } catch (e) {
      setDoctorError(String(e))
    } finally {
      setDoctorRunning(false)
    }
  }

  const save = async (): Promise<boolean> => {
    if (!form || !rawDoc) return false
    setSaving(true)
    setError('')
    try {
      const data: Record<string, unknown> = {
        ...(rawDoc.data ?? {}),
        name: form.name,
        creator: form.creator,
        character_version: form.character_version,
        description: form.description,
        personality: form.personality,
        scenario: form.scenario,
        first_mes: form.first_mes,
        // Drop blank greetings the author added but never filled.
        alternate_greetings: form.alternate_greetings.map((g) => g).filter((g) => g.trim() !== ''),
        mes_example: form.mes_example,
        system_prompt: form.system_prompt,
        post_history_instructions: form.post_history_instructions,
        creator_notes: form.creator_notes,
        tags: form.tags,
      }
      const doc = { ...rawDoc, data }
      if (!cardId) {
        // First save of a new character: there is no stored card to edit, so
        // import the document we have been assembling. The id the store mints
        // is content-derived, so it only exists once there IS content.
        const v = await client.send<CardView>('cards.import', { bytes: encodeCardDoc(doc) })
        if (!v?.id) throw new Error(t('the card was not stored'))
        setCardId(v.id)
        onCreated?.(v.id)
        setSavedAt(true)
        lint(v.id)
        onSaved()
        return true
      }
      await client.send('cards.edit', { id: cardId, card: doc })
      setSavedAt(true)
      lint()
      onSaved()
      return true
    } catch (e) {
      setError(String(e))
      return false
    } finally {
      setSaving(false)
    }
  }

  const warns = findings?.filter((f) => f.severity === 'warn').length ?? 0
  const infos = (findings?.length ?? 0) - warns

  // A proposal's severity is a wire token ('warn'/'info'/'suggestion'): the CSS
  // class uses it raw, but the displayed label is translated per known token.
  const sevLabel = (s: string) => (s === 'warn' ? t('warn') : s === 'info' ? t('info') : s === 'suggestion' ? t('suggestion') : s)

  // A screen, not a modal. The editor used to be a sheet over whichever surface
  // opened it, and the two hosts disagreed about what a save meant — one closed
  // it mid-consultation. It now has one home (the character studio) that both
  // the Library and a session navigate INTO, so there is one lifecycle instead
  // of one per caller.
  // A card reached by deep link is known only by its id until cards.get lands, so
  // "no name yet" must not read as "new" — the header would announce a character
  // that plainly exists as one being created.
  const existing = Boolean(card || cardId)
  const displayName = form?.name || card?.name || (existing ? t('Character') : t('New character'))
  return (
    <div class="stage-cardeditor stage-cardeditor--screen">
      <header class="stage-cardsheet__head">
        {card?.avatar_url ? (
          <img class="stage-cardsheet__avatar" src={card.avatar_url} alt="" />
        ) : (
          <div class="stage-cardsheet__avatar stage-cardsheet__avatar--blank" aria-hidden="true">
            {(displayName[0] ?? '·').toUpperCase()}
          </div>
        )}
        <div class="stage-cardsheet__id">
          <h3>{existing ? t('Edit — %s', displayName) : t('New character')}</h3>
          <div class="stage-cardsheet__meta">
            <span>
              {existing
                ? t('Changes save to your library; the avatar and id stay put.')
                : t('Give them a name and save — then the lint and the doctor can read them.')}
            </span>
          </div>
        </div>
      </header>

        {error && (
          <p class="stage-error" onClick={() => setError('')}>
            {error}
          </p>
        )}

        {findings && findings.length > 0 && (
          <div class="stage-lint stage-cardeditor__lint">
            <div class="stage-lint__head">
              <span>{t('Findings')}</span>
              <span class="stage-lint__counts">
                {warns > 0 && <span class="stage-lint__count stage-lint__count--warn">⚠ {warns}</span>}
                {infos > 0 && <span class="stage-lint__count stage-lint__count--info">ⓘ {infos}</span>}
              </span>
            </div>
            <ul class="stage-lint__list">
              {findings.map((f, i) => (
                <li key={i} class={`stage-lint__item stage-lint__item--${f.severity === 'warn' ? 'warn' : 'info'}`}>
                  <span class="stage-lint__sev" aria-hidden="true">
                    {f.severity === 'warn' ? '⚠' : 'ⓘ'}
                  </span>
                  <div class="stage-lint__body">
                    <span class="stage-lint__msg">{f.message}</span>
                    {(f.field || f.detail) && (
                      <span class="stage-lint__where">
                        {f.field && <span class="stage-lint__field">{f.field}</span>}
                        {f.detail && <code class="stage-lint__detail">{f.detail}</code>}
                      </span>
                    )}
                  </div>
                </li>
              ))}
            </ul>
          </div>
        )}
        {findings && findings.length === 0 && <p class="stage-lint__clean">{t('✓ No lint findings.')}</p>}

        <div class="stage-doctor">
          <div class="stage-doctor__head">
            <div class="stage-doctor__title">
              <span class="stage-doctor__emoji" aria-hidden="true">
                🛠️
              </span>
              <span>{t('Card doctor')}</span>
              <Hint
                text={t('Seppä, the card-craft persona, reads the card and its lint and suggests concrete edits you can apply.')}
                label={t('about the card doctor')}
              />
            </div>
            {!proposals && (
              // The doctor reads the STORED card, so it has nothing to read
              // until a new character has been saved once. Disabled and said
              // plainly, rather than offered and then failing on an id that
              // does not exist.
              <button class="stage-doctor__run" disabled={doctorRunning || !form || !cardId} onClick={() => void runDoctor()}>
                {doctorRunning ? t('Consulting…') : !cardId ? t('Save first') : t('Ask the doctor')}
              </button>
            )}
          </div>
          {/* Say what you want out of this pass. The doctor works toward it
              first — including cutting things — instead of only answering the
              lint. Left in place between rounds on purpose: it is the standing
              brief for the consultation, and an author who changes their mind
              edits it rather than starting over. */}
          <label class="stage-doctor__steer">
            <span class="stage-doctor__steerlabel">
              {t('What should change?')}
              <Hint
                text={t('Optional. Tell the doctor what you are after — “make her wearier”, “cut the war backstory”, “she should not know about the Accord yet” — and it proposes edits toward that first. Leave it empty for an ordinary read of the card and its lint.')}
                label={t('about steering the doctor')}
              />
            </span>
            <textarea
              class="stage-editfield__area stage-doctor__steerbox"
              rows={2}
              placeholder={t('e.g. make her wearier, and cut the war backstory')}
              value={steer}
              onInput={(e) => setSteer((e.target as HTMLTextAreaElement).value)}
            />
          </label>
          <div class="stage-doctor__model">
            <ModelPick
              client={client}
              sessionId=""
              currentProvider={ovProvider}
              currentModel={ovModel}
              onSelect={(p, m) => {
                setOvProvider(p)
                setOvModel(m)
              }}
              defaultLabel={t('Workspace model')}
            />
          </div>
          {doctorError && (
            <p class="stage-error" onClick={() => setDoctorError('')}>
              {doctorError}
            </p>
          )}
          {doctorNote && <p class="stage-doctor__note">{doctorNote}</p>}
          {proposals && proposals.length === 0 && !doctorNote && <p class="stage-doctor__note">{t('No changes suggested — the card looks good.')}</p>}
          {proposals && proposals.length > 0 && (
            <>
              <ul class="stage-doctor__list">
                {proposals.map((p) => (
                  <li key={p.id} class="stage-doctor__item">
                    <div class="stage-doctor__meta">
                      <span class={`stage-doctor__sev stage-doctor__sev--${p.severity}`}>{sevLabel(p.severity)}</span>
                      <span class="stage-doctor__field">{tr(FIELD_LABELS[p.field] ?? p.field)}</span>
                    </div>
                    {p.rationale && <p class="stage-doctor__why">{p.rationale}</p>}
                    <div class="stage-doctor__diff">
                      <div class="stage-doctor__before">
                        <span class="stage-doctor__difflabel">{t('Now')}</span>
                        <p>{p.before?.trim() || t('(empty)')}</p>
                      </div>
                      {/* A removal's `after` is empty by design, so render what
                          it MEANS — an empty box below "Proposed" reads as a
                          broken suggestion rather than a deletion. */}
                      <div class={`stage-doctor__after ${p.remove ? 'stage-doctor__after--remove' : ''}`}>
                        <span class="stage-doctor__difflabel">{p.remove ? t('Proposed — remove') : t('Proposed')}</span>
                        <p>{p.remove ? t('(this field is cleared)') : p.after}</p>
                      </div>
                    </div>
                    {applied[p.id] ? (
                      <div class="stage-doctor__verdict stage-doctor__verdict--applied">{t('Applied ✓ — save to keep')}</div>
                    ) : declined[p.id] !== undefined ? (
                      <div class="stage-doctor__verdict stage-doctor__verdict--declined">
                        <span>{t('Declined')}{declined[p.id] ? `: ${declined[p.id]}` : ''}</span>
                        <button class="stage-doctor__link" onClick={() => applyProposal(p)}>
                          {t('apply instead')}
                        </button>
                      </div>
                    ) : decliningId === p.id ? (
                      <div class="stage-doctor__decline">
                        <textarea
                          class="stage-editfield__area stage-doctor__reason"
                          rows={2}
                          placeholder={t('Why? (e.g. keep the backstory) — the doctor weighs this next pass')}
                          value={reasonDraft}
                          onInput={(e) => setReasonDraft((e.target as HTMLTextAreaElement).value)}
                        />
                        <div class="stage-doctor__declineactions">
                          <button
                            class="stage-doctor__link"
                            onClick={() => {
                              setDecliningId(null)
                              setReasonDraft('')
                            }}
                          >
                            {t('cancel')}
                          </button>
                          <button class="stage-doctor__apply" onClick={() => confirmDecline(p.id)}>
                            {t('Confirm decline')}
                          </button>
                        </div>
                      </div>
                    ) : (
                      <div class="stage-doctor__actions">
                        <button class="stage-doctor__apply" onClick={() => applyProposal(p)}>
                          {t('Apply')}
                        </button>
                        <button
                          class="stage-doctor__declinebtn"
                          onClick={() => {
                            setDecliningId(p.id)
                            setReasonDraft('')
                          }}
                        >
                          {t('Decline')}
                        </button>
                      </div>
                    )}
                  </li>
                ))}
              </ul>
              <div class="stage-doctor__footer">
                {proposals.some((p) => !applied[p.id] && declined[p.id] === undefined) && (
                  <button class="stage-doctor__link" onClick={applyAll}>
                    {t('Apply all')}
                  </button>
                )}
                <button class="stage-doctor__revise" disabled={doctorRunning || saving} onClick={() => void reviseDoctor()}>
                  {doctorRunning ? t('Consulting…') : t('Save & ask again')}
                </button>
              </div>
            </>
          )}
        </div>

        {!form && !error && <p class="stage-empty">{t('Loading…')}</p>}
        {form && (
          <div class="stage-cardeditor__fields">
            <div class="stage-cardeditor__row">
              <EditField label={t('Name')} value={form.name} hint={t("The {{char}} macro and the card's display name.")} onInput={(v) => set('name', v)} />
              <EditField label={t('Version')} value={form.character_version} hint={t("The card author's version string, if any.")} onInput={(v) => set('character_version', v)} />
            </div>
            <EditField label={t('Creator')} value={form.creator} hint={t('Who authored the card.')} onInput={(v) => set('creator', v)} />
            <EditField
              label={t('Description')}
              value={form.description}
              area
              rows={6}
              hint={t("The character's core prompt — baked into the cached prefix every turn.")}
              onInput={(v) => set('description', v)}
            />
            <EditField
              label={t('Personality')}
              value={form.personality}
              area
              hint={t('A short personality summary. Many cards fold this into the description instead.')}
              onInput={(v) => set('personality', v)}
            />
            <EditField label={t('Scenario')} value={form.scenario} area hint={t('The situation the roleplay opens in.')} onInput={(v) => set('scenario', v)} />
            <EditField
              label={t('First message')}
              value={form.first_mes}
              area
              rows={5}
              hint={t('The opening line (greeting 1). Supports {{char}} / {{user}} macros.')}
              onInput={(v) => set('first_mes', v)}
            />

            <div class="stage-editfield">
              <span class="stage-editfield__label">
                {t('Alternate greetings')}
                <Hint text={t('Extra openings the reader can swipe between at the start of a chat.')} label={t('alternate greetings')} />
              </span>
              {form.alternate_greetings.map((g, i) => (
                <div key={i} class="stage-cardeditor__greeting">
                  <textarea
                    class="stage-editfield__area"
                    rows={3}
                    value={g}
                    onInput={(e) => setGreeting(i, (e.target as HTMLTextAreaElement).value)}
                  />
                  <button class="stage-cardeditor__greeting-del" title={t('Remove this greeting')} onClick={() => removeGreeting(i)}>
                    ✕
                  </button>
                </div>
              ))}
              <button class="stage-cardeditor__add" onClick={addGreeting}>
                {t('+ Add greeting')}
              </button>
            </div>

            <EditField
              label={t('Example dialogue')}
              value={form.mes_example}
              area
              rows={5}
              hint={t("Sample exchanges that teach the character's voice.")}
              onInput={(v) => set('mes_example', v)}
            />
            <EditField
              label={t('System prompt')}
              value={form.system_prompt}
              area
              hint={t('An override system prompt the card requests, if any.')}
              onInput={(v) => set('system_prompt', v)}
            />
            <EditField
              label={t('Post-history instructions')}
              value={form.post_history_instructions}
              area
              hint={t('Appended AFTER the chat history each turn — strong, recency-weighted steering.')}
              onInput={(v) => set('post_history_instructions', v)}
            />
            <EditField
              label={t('Creator notes')}
              value={form.creator_notes}
              area
              hint={t('Shown to you only — never sent to the model.')}
              onInput={(v) => set('creator_notes', v)}
            />
            <EditField
              label={t('Tags')}
              value={form.tags.join(', ')}
              hint={t('Comma-separated labels for browsing; not sent to the model.')}
              onInput={(v) => set('tags', v.split(',').map((x) => x.trim()).filter((x) => x !== ''))}
            />
          </div>
        )}

      <div class="stage-cardeditor__bar">
        {savedAt && !saving && <span class="stage-cardeditor__saved">{t('Saved ✓')}</span>}
        <button class="stage-cardeditor__cancel" onClick={onClose}>
          {t('Close')}
        </button>
        <button class="stage-cardeditor__save" disabled={saving || !form} onClick={() => void save()}>
          {saving ? t('Saving…') : existing ? t('Save changes') : t('Create character')}
        </button>
      </div>
    </div>
  )
}
