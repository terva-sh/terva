import { errText } from '../../platform/ctrlproto/errors'
import { useEffect, useState } from 'preact/hooks'
import type { ClientLike } from '../../platform/ctrlproto/client'
import type { CardView, DoctorDecision, SessionDoctorResult, SessionProposal } from '../../platform/ctrlproto/types'
import { t } from '../../i18n'
import { ModelPick } from './ModelPick'

// The session doctor (SD1–SD4 — Dramaturgi): read the played session and
// propose the structure it has earned — lore entries, open threads, cast
// promotions, scene-state updates — in the card doctor's negotiation shape. Nothing applies
// without an explicit accept; a decline's reason rides the next round; a
// promotion opens prefilled fields to review, never a silent import.
//
// Two chromes, one engine: the Steering World-tab section runs the
// whole-session sweep on demand (SD1); the Chat overlay runs a narrowed ask —
// mark ONE message as lore (focus, SD2) or promote a named walk-on
// (promote, SD3) — launched from a message's edit actions.

// base64 for a unicode JSON payload (btoa alone dies on non-Latin-1).
function jsonToBase64(v: unknown): string {
  const bytes = new TextEncoder().encode(JSON.stringify(v))
  let binary = ''
  for (let i = 0; i < bytes.length; i++) binary += String.fromCharCode(bytes[i])
  return btoa(binary)
}

type Verdict = 'applying' | 'accepted' | 'declined'
type PromoDraft = { character: string; description: string; personality: string; first_mes: string }

// DoctorAsk narrows a run: SD2's marked message or SD3's walk-on. Absent =
// the whole-session sweep.
export type DoctorAsk = { focus: number } | { promote: string }

function DoctorProposals(props: {
  client: ClientLike
  sessionId: string
  ask?: DoctorAsk
  autorun?: boolean
  // The session's provider/model, so the picker can name what "Session model"
  // (the daemon's default for this call) actually resolves to.
  defaultProvider?: string
  defaultModel?: string
  // Accepting a scene_break opens the next-scene flow instead of writing
  // anything — the one proposal kind with no verb of its own. Absent where
  // that flow can't be reached, in which case the kind is not offered.
  onSceneBreak?: (suggestedTitle: string) => void
}) {
  const { client, sessionId, ask } = props
  const [running, setRunning] = useState(false)
  const [note, setNote] = useState('')
  const [proposals, setProposals] = useState<SessionProposal[] | null>(null)
  const [verdicts, setVerdicts] = useState<Record<string, Verdict>>({})
  const [reasons, setReasons] = useState<Record<string, string>>({})
  const [declining, setDeclining] = useState<Record<string, boolean>>({})
  const [promoDrafts, setPromoDrafts] = useState<Record<string, PromoDraft>>({})
  const [error, setError] = useState('')
  // Per-generation model override; empty = the session model (the daemon default).
  const [ovProvider, setOvProvider] = useState('')
  const [ovModel, setOvModel] = useState('')

  const run = (decisions?: DoctorDecision[]) => {
    setRunning(true)
    setError('')
    client
      .send<SessionDoctorResult>('sessions.doctor', { decisions, provider: ovProvider, model: ovModel, ...(ask ?? {}) }, sessionId)
      .then((r) => {
        // A scene_break has nowhere to go without the next-scene flow, and a
        // proposal you cannot accept is worse than none (the SD1 rule).
        setProposals((r.proposals ?? []).filter((p) => p.kind !== 'scene_break' || props.onSceneBreak))
        setNote(r.note ?? '')
        setVerdicts({})
        setReasons({})
        setDeclining({})
        const drafts: Record<string, PromoDraft> = {}
        for (const p of r.proposals ?? []) {
          if (p.kind === 'cast_promotion') {
            drafts[p.id] = {
              character: p.character ?? '',
              description: p.description ?? '',
              personality: p.personality ?? '',
              first_mes: p.first_mes ?? '',
            }
          }
        }
        setPromoDrafts(drafts)
      })
      .catch((e: unknown) => setError(errText(e)))
      .finally(() => setRunning(false))
  }

  // The overlay runs its narrowed ask on open; the section waits for the
  // button press.
  useEffect(() => {
    if (props.autorun) run()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  const setVerdict = (id: string, v: Verdict | undefined) =>
    setVerdicts((m) => {
      const next = { ...m }
      if (v === undefined) delete next[id]
      else next[id] = v
      return next
    })

  const acceptLore = (p: SessionProposal) => {
    // name and content are optional on a proposal because the kinds carry
    // different fields, but a lore entry requires both. Today only the kinds
    // that carry them route here; sending anyway would omit the keys from the
    // JSON and write a nameless entry, which the author would find later as a
    // lore row with no title and no way to reference it.
    if (!p.name || !p.content) {
      setVerdict(p.id, undefined)
      setError(t('That proposal arrived without a name or body — nothing was written.'))
      return
    }
    setVerdict(p.id, 'applying')
    client
      .send(
        'world.lore.put',
        {
          entry: {
            name: p.name,
            keys: p.keys && p.keys.length > 0 ? p.keys : undefined,
            constant: !p.keys || p.keys.length === 0,
            content: p.content,
            audience: p.audience && p.audience.length > 0 ? p.audience : undefined,
          },
        },
        sessionId,
      )
      .then(() => setVerdict(p.id, 'accepted'))
      .catch((e: unknown) => {
        setVerdict(p.id, undefined)
        setError(errText(e))
      })
  }

  // The only accept that removes something. No confirm dialog: the proposal
  // itself is the confirmation — the author read the name and the rationale
  // and pressed a button that says Retire.
  const acceptRetire = (p: SessionProposal) => {
    // A delete keyed on an absent name would ask the daemon to retire "" —
    // refuse rather than send it.
    if (!p.name) {
      setVerdict(p.id, undefined)
      setError(t('That proposal arrived without a name — nothing was retired.'))
      return
    }
    setVerdict(p.id, 'applying')
    client
      .send('world.lore.delete', { name: p.name }, sessionId)
      .then(() => setVerdict(p.id, 'accepted'))
      .catch((e: unknown) => {
        setVerdict(p.id, undefined)
        setError(errText(e))
      })
  }

  const acceptPromotion = (p: SessionProposal) => {
    const d = promoDrafts[p.id]
    if (!d || !d.character.trim() || !d.description.trim()) return
    setVerdict(p.id, 'applying')
    client
      .send<CardView>('cards.import', {
        bytes: jsonToBase64({
          name: d.character.trim(),
          description: d.description,
          personality: d.personality || undefined,
          first_mes: d.first_mes || undefined,
        }),
      })
      .then((card) => client.send('cast.add', { name: d.character.trim(), ref: card.id }, sessionId))
      .then(() => setVerdict(p.id, 'accepted'))
      .catch((e: unknown) => {
        setVerdict(p.id, undefined)
        setError(errText(e))
      })
  }

  const decline = (p: SessionProposal) => {
    setVerdict(p.id, 'declined')
    setDeclining((m) => ({ ...m, [p.id]: false }))
  }

  // The negotiation: every decided proposal rides back — accepted ones so the
  // doctor keeps them out of the next list, declines with their reasons.
  const revise = () => {
    const decisions: DoctorDecision[] = (proposals ?? [])
      .filter((p) => verdicts[p.id] === 'accepted' || verdicts[p.id] === 'declined')
      .map((p) => ({
        proposal_id: p.id,
        field: p.kind + (p.name ? ':' + p.name : p.character ? ':' + p.character : ''),
        accepted: verdicts[p.id] === 'accepted',
        reason: verdicts[p.id] === 'declined' ? reasons[p.id] || undefined : undefined,
      }))
    run(decisions)
  }

  const kindLabel = (k: SessionProposal['kind']) =>
    k === 'lore_entry'
      ? t('📖 lore')
      : k === 'open_thread'
        ? t('🧵 thread')
        : k === 'scene_state'
          ? t('📌 scene state')
          : k === 'scene_break'
            ? t('🎬 scene break')
            : k === 'lore_retire'
              ? t('🗑 retire')
              : t('🎭 promotion')

  // The promo field keys are wire names; their displayed labels are translated
  // per key (the underscore-to-space form is the English source).
  const promoFieldLabel = (f: keyof PromoDraft) =>
    f === 'character' ? t('character') : f === 'description' ? t('description') : f === 'personality' ? t('personality') : t('first mes')

  const undecided = (proposals ?? []).filter((p) => !verdicts[p.id]).length

  // Which model the doctor runs on — the session's by default, or a per-run
  // override. Takes effect on the next run/revise. Shown in both chromes so the
  // billed call always names the model it will spend on.
  const picker = (
    <ModelPick
      client={client}
      sessionId={sessionId}
      currentProvider={ovProvider}
      currentModel={ovModel}
      onSelect={(p, m) => {
        setOvProvider(p)
        setOvModel(m)
      }}
      defaultLabel={t('Session model')}
      defaultProvider={props.defaultProvider}
      defaultModel={props.defaultModel}
    />
  )

  if (proposals === null && !props.autorun) {
    return (
      <>
        {picker}
        <button class="stage-doctor__run" disabled={running} onClick={() => run()}>
          {running ? t('Reading the scene…') : t('🩺 Doctor this session')}
        </button>
      </>
    )
  }
  return (
    <>
      {picker}
      {proposals === null && <p class="stage-hint">{t('Reading the scene…')}</p>}
      {note && <p class="stage-doctor__note">{note}</p>}
      {proposals?.length === 0 && !note && <p class="stage-hint">{t('Nothing to propose yet.')}</p>}
      <ul class="stage-doctor__list">
        {(proposals ?? []).map((p) => (
          <li key={p.id} class={`stage-doctor__item stage-doctor__item--${verdicts[p.id] ?? 'open'}`}>
            <div class="stage-doctor__head">
              <span class="stage-doctor__kind">{kindLabel(p.kind)}</span>
              <strong>{p.kind === 'cast_promotion' ? p.character : p.name}</strong>
              {p.kind === 'scene_state' ? (
                // The pin is always-on and shared by definition; "replaces the
                // card" is the fact the author is deciding on.
                <span class="stage-doctor__scope">{t('replaces the pinned card')}</span>
              ) : p.kind === 'scene_break' ? (
                <span class="stage-doctor__scope">{t('opens a new scene')}</span>
              ) : p.kind === 'lore_retire' ? (
                <span class="stage-doctor__scope">{t('removes this entry')}</span>
              ) : (
                p.kind !== 'cast_promotion' && (
                  <span class="stage-doctor__scope">
                    {p.audience && p.audience.length > 0 ? `🔒 ${p.audience.join(', ')}` : t('shared')}
                  </span>
                )
              )}
              {verdicts[p.id] === 'accepted' && <span class="stage-doctor__done">{t('✓ applied')}</span>}
              {verdicts[p.id] === 'declined' && <span class="stage-doctor__done">{t('✗ declined')}</span>}
            </div>
            <p class="stage-doctor__rationale">{p.rationale}</p>
            {p.kind !== 'cast_promotion' && <p class="stage-doctor__content">{p.content}</p>}
            {p.kind !== 'cast_promotion' && p.keys && p.keys.length > 0 && (
              <p class="stage-doctor__keys">{t('keys: %s', p.keys.join(', '))}</p>
            )}
            {p.kind === 'cast_promotion' && !verdicts[p.id] && promoDrafts[p.id] && (
              <div class="stage-doctor__promo">
                {(['character', 'description', 'personality', 'first_mes'] as const).map((f) => (
                  <label key={f} class="stage-doctor__field">
                    <span>{promoFieldLabel(f)}</span>
                    <textarea
                      rows={f === 'character' ? 1 : 2}
                      value={promoDrafts[p.id][f]}
                      onInput={(e) =>
                        setPromoDrafts((m) => ({
                          ...m,
                          [p.id]: { ...m[p.id], [f]: (e.target as HTMLTextAreaElement).value },
                        }))
                      }
                    />
                  </label>
                ))}
              </div>
            )}
            {!verdicts[p.id] && (
              <div class="stage-doctor__acts">
                {p.kind === 'cast_promotion' ? (
                  <button class="stage-doctor__accept" onClick={() => acceptPromotion(p)}>
                    {t('Add to library & stage')}
                  </button>
                ) : p.kind === 'scene_break' ? (
                  // Nothing is written here: this hands off to the next-scene
                  // flow, which drafts the recap and cold open for review.
                  <button class="stage-doctor__accept" onClick={() => props.onSceneBreak?.(p.name ?? '')}>
                    {t('End the scene here…')}
                  </button>
                ) : p.kind === 'lore_retire' ? (
                  <button class="stage-doctor__accept stage-doctor__accept--destructive" onClick={() => acceptRetire(p)}>
                    {t('Retire it')}
                  </button>
                ) : (
                  <button class="stage-doctor__accept" onClick={() => acceptLore(p)}>
                    {t('Accept')}
                  </button>
                )}
                <button class="stage-doctor__decline" onClick={() => setDeclining((m) => ({ ...m, [p.id]: !m[p.id] }))}>
                  {t('Decline…')}
                </button>
              </div>
            )}
            {declining[p.id] && !verdicts[p.id] && (
              <div class="stage-doctor__reason">
                <input
                  placeholder={t('Why not? The doctor honors the reason next round.')}
                  value={reasons[p.id] ?? ''}
                  onInput={(e) => setReasons((m) => ({ ...m, [p.id]: (e.target as HTMLInputElement).value }))}
                />
                <button onClick={() => decline(p)}>{t('Decline')}</button>
              </div>
            )}
          </li>
        ))}
      </ul>
      {proposals !== null && (
        <div class="stage-doctor__round">
          <button class="stage-doctor__revise" disabled={running} onClick={revise} title={t('Send your decisions back for a revised round')}>
            {running ? t('Reading the scene…') : undecided > 0 ? t('Revise with my decisions') : t('Ask again')}
          </button>
          {!props.autorun && (
            <button class="stage-doctor__close" onClick={() => setProposals(null)}>
              {t('Done')}
            </button>
          )}
        </div>
      )}
      {error && <p class="stage-doctor__error">{error}</p>}
    </>
  )
}

// The Steering World-tab section (SD1): the whole-session sweep.
export function SessionDoctor(props: {
  client: ClientLike
  sessionId: string
  defaultProvider?: string
  defaultModel?: string
  onSceneBreak?: (suggestedTitle: string) => void
}) {
  return (
    <section class="stage-drawer__section stage-doctor">
      <h4>{t('Session doctor')}</h4>
      <p class="stage-hint">
        {t('Dramaturgi reads the whole scene and proposes what it has earned — lore worth keeping, promised callbacks, a walk-on ready for the cast, an up-to-date scene-state card. Nothing applies unless you accept it. One bounded model call, billed to this session.')}
      </p>
      <DoctorProposals
        client={props.client}
        sessionId={props.sessionId}
        defaultProvider={props.defaultProvider}
        defaultModel={props.defaultModel}
        onSceneBreak={props.onSceneBreak}
      />
    </section>
  )
}

// The Chat overlay (SD2/SD3): a narrowed ask launched from a message's edit
// actions — keep this moment as lore, or promote this walk-on. Runs on open.
export function DoctorOverlay(props: {
  client: ClientLike
  sessionId: string
  ask: DoctorAsk
  defaultProvider?: string
  defaultModel?: string
  onClose: () => void
}) {
  const heading = 'focus' in props.ask ? t('Keep this moment as lore') : t('Promote %s to the cast', props.ask.promote)
  return (
    <div class="stage-sheet-backdrop" onClick={props.onClose}>
      <div class="stage-doctor-sheet stage-doctor" onClick={(e) => e.stopPropagation()}>
        <header class="stage-doctor-sheet__head">
          <h3>{heading}</h3>
          <button class="stage-drawer__close" onClick={props.onClose}>
            ✕
          </button>
        </header>
        <DoctorProposals
          client={props.client}
          sessionId={props.sessionId}
          ask={props.ask}
          defaultProvider={props.defaultProvider}
          defaultModel={props.defaultModel}
          autorun
        />
      </div>
    </div>
  )
}