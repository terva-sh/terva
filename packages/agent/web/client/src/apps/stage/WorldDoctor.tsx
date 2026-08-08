import { useState } from 'preact/hooks'
import type { ClientLike } from '../../platform/ctrlproto/client'
import type {
  CardView,
  DoctorDecision,
  SessionInfo,
  SessionProposal,
  WorldCardProposal,
  WorldDoctorResult,
  WorldsCreateCharacterResult,
  WorldView,
} from '../../platform/ctrlproto/types'
import { t } from '../../i18n'
import { ModelPick } from './ModelPick'
import { applyCardFields } from './cardraw'

// The world doctor (SD6 — Dramaturgi over the ensemble): read the whole cast
// together with the World's lorebook, and propose character edits beside world
// edits — plus the character the cast is missing.
//
// The negotiation shape is the doctors' and is deliberately NOT the realize
// sheet's accept-all. Realize is a one-shot harvest with nothing to answer;
// here every verdict rides back in `decisions`, so a per-proposal accept or
// decline-with-reason is what makes the second round different from the first.
// Volume is handled by GROUPING instead — the cast's proposals sit under the
// character they are about — rather than by removing the decisions the
// negotiation runs on.

type Verdict = 'applying' | 'accepted' | 'declined'
type NewDraft = { character: string; description: string; personality: string; first_mes: string }

// A card proposal and a world proposal share an id space only by accident, so
// verdicts are keyed with a prefix. Without it a card proposal "p1" and a world
// proposal "p1" would decide each other — and the doctor names them
// independently, so the collision is likely rather than theoretical.
const cardKey = (id: string) => 'c:' + id
const worldKey = (id: string) => 'w:' + id

// How many scenes are pre-checked. A World with a long history should not cost
// a full read by default; the server caps how many it will read at all.
const DEFAULT_SCENES = 3

export function WorldDoctor(props: {
  client: ClientLike
  // The saved World under review. Everything here reads and writes it by id,
  // with no session anywhere — which is the whole reason the doctor moved out
  // of the steering drawer and onto this screen.
  world: WorldView
  // The scenes played in this World, offered as evidence to pick from.
  scenes: SessionInfo[]
  // Applied changes land in the saved World, so the screen holding it re-renders
  // from the verb's own answer.
  onWorld: (w: WorldView) => void
  defaultProvider?: string
  defaultModel?: string
}) {
  const { client } = props
  const worldId = props.world.id
  const [running, setRunning] = useState(false)
  const [note, setNote] = useState('')
  const [cards, setCards] = useState<WorldCardProposal[] | null>(null)
  const [world, setWorld] = useState<SessionProposal[]>([])
  const [verdicts, setVerdicts] = useState<Record<string, Verdict>>({})
  const [reasons, setReasons] = useState<Record<string, string>>({})
  const [declining, setDeclining] = useState<Record<string, boolean>>({})
  const [drafts, setDrafts] = useState<Record<string, NewDraft>>({})
  const [steer, setSteer] = useState('')
  const [error, setError] = useState('')
  const [ovProvider, setOvProvider] = useState('')
  const [ovModel, setOvModel] = useState('')
  // Which scenes inform the run. Pre-checked to the most recent few: a World
  // with a long history should not cost a full read by default, and the nights
  // that matter are usually the recent ones — but which they are is a judgement,
  // so it is a choice rather than a rule.
  //
  // null means UNTOUCHED, and the default is DERIVED from the scenes currently
  // in hand rather than seeded once at mount. That distinction is the whole
  // reason this is not a plain useState initializer: the studio loads its
  // sessions asynchronously, so the first render has no scenes at all, and a
  // seed taken then would leave every box unchecked forever — a run that
  // silently read nothing while the picker claimed otherwise.
  const [picked, setPicked] = useState<Record<string, boolean> | null>(null)
  const isPicked = (id: string) =>
    picked ? !!picked[id] : props.scenes.slice(0, DEFAULT_SCENES).some((s) => s.id === id)
  const chosen = props.scenes.filter((s) => isPicked(s.id)).map((s) => s.id)
  // The first interaction materializes the derived default, so unchecking one
  // box does not also discard the other two.
  const togglePicked = (id: string, on: boolean) =>
    setPicked((m) => {
      const base = m ?? Object.fromEntries(props.scenes.slice(0, DEFAULT_SCENES).map((s) => [s.id, true]))
      return { ...base, [id]: on }
    })

  const setVerdict = (key: string, v: Verdict | undefined) =>
    setVerdicts((m) => {
      const next = { ...m }
      if (v === undefined) delete next[key]
      else next[key] = v
      return next
    })

  const run = (decisions?: DoctorDecision[]) => {
    setRunning(true)
    setError('')
    client
      // The steer is re-sent on every round, revises included: it is standing
      // direction for the pass, not a one-time question. An author who edits it
      // mid-negotiation is changing course, and the next round must hear that.
      .send<WorldDoctorResult>('worlds.doctor', {
        id: worldId,
        sessions: chosen,
        decisions,
        steer: steer.trim() || undefined,
        provider: ovProvider,
        model: ovModel,
      })
      .then((r) => {
        setCards(r.card_proposals ?? [])
        setWorld(r.world_proposals ?? [])
        setNote(r.note ?? '')
        setVerdicts({})
        setReasons({})
        setDeclining({})
        const next: Record<string, NewDraft> = {}
        for (const p of r.world_proposals ?? []) {
          if (p.kind === 'character_new') {
            next[p.id] = {
              character: p.character ?? '',
              description: p.description ?? '',
              personality: p.personality ?? '',
              first_mes: p.first_mes ?? '',
            }
          }
        }
        setDrafts(next)
      })
      .catch((e: unknown) => setError(String(e)))
      .finally(() => setRunning(false))
  }

  // Apply one card edit — through worlds.edit_character, NOT cards.edit.
  //
  // That is the difference between an accepted proposal changing this World's
  // version of a character and changing the character every other World is
  // still playing. The verb forks and re-points the roster; the shared library
  // card is never opened for writing.
  //
  // The card is re-read from the ROSTER on every accept rather than once per
  // round, which is what makes two accepted proposals on the same character
  // compose: the first fork moved the roster's ref, and the second has to edit
  // the fork rather than the original it was made from.
  const acceptCard = async (p: WorldCardProposal) => {
    setVerdict(cardKey(p.id), 'applying')
    try {
      const ref = props.world.characters?.[p.character] ?? p.card
      const view = await client.send<CardView>('cards.get', { id: ref })
      const res = await client.send<{ world: WorldView }>('worlds.edit_character', {
        id: worldId,
        character: p.character,
        card: applyCardFields(view.raw, { [p.field]: p.after }),
      })
      props.onWorld(res.world)
      setVerdict(cardKey(p.id), 'accepted')
    } catch (e) {
      setVerdict(cardKey(p.id), undefined)
      setError(String(e))
    }
  }

  const acceptLore = (p: SessionProposal) => {
    if (!p.name || !p.content) {
      setVerdict(worldKey(p.id), undefined)
      setError(t('That proposal arrived without a name or body — nothing was written.'))
      return
    }
    setVerdict(worldKey(p.id), 'applying')
    client
      .send<WorldView>(
        'worlds.lore.put',
        {
          id: worldId,
          entry: {
            name: p.name,
            keys: p.keys && p.keys.length > 0 ? p.keys : undefined,
            constant: !p.keys || p.keys.length === 0,
            content: p.content,
            audience: p.audience && p.audience.length > 0 ? p.audience : undefined,
          },
        },
      )
      .then((w) => {
        props.onWorld(w)
        setVerdict(worldKey(p.id), 'accepted')
      })
      .catch((e: unknown) => {
        setVerdict(worldKey(p.id), undefined)
        setError(String(e))
      })
  }

  const acceptRetire = (p: SessionProposal) => {
    if (!p.name) {
      setVerdict(worldKey(p.id), undefined)
      setError(t('That proposal arrived without a name — nothing was retired.'))
      return
    }
    setVerdict(worldKey(p.id), 'applying')
    client
      .send<WorldView>('worlds.lore.delete', { id: worldId, name: p.name })
      .then((w) => {
        props.onWorld(w)
        setVerdict(worldKey(p.id), 'accepted')
      })
      .catch((e: unknown) => {
        setVerdict(worldKey(p.id), undefined)
        setError(String(e))
      })
  }

  // A new character is imported and staged from the EDITED draft, never the
  // proposal as it arrived — the same rule cast_promotion follows, and the
  // reason the fields are a form rather than a preview.
  const acceptNew = (p: SessionProposal) => {
    const d = drafts[p.id]
    if (!d || !d.character.trim() || !d.description.trim()) return
    setVerdict(worldKey(p.id), 'applying')
    // ONE verb. This was cards.import followed by worlds.add_character, which
    // could half-apply — the import lands, the roster call fails, and the author
    // is left with a character in their library that no World asked for. It also
    // left provenance to the client, so a card born in a World arrived on the
    // shelf indistinguishable from one the author made themselves.
    client
      .send<WorldsCreateCharacterResult>('worlds.create_character', {
        id: worldId,
        name: d.character.trim(),
        card: {
          name: d.character.trim(),
          description: d.description,
          personality: d.personality || undefined,
          first_mes: d.first_mes || undefined,
        },
      })
      .then((res) => {
        props.onWorld(res.world)
        setVerdict(worldKey(p.id), 'accepted')
      })
      .catch((e: unknown) => {
        setVerdict(worldKey(p.id), undefined)
        setError(String(e))
      })
  }

  // Every decided proposal from BOTH families rides back — the doctor keeps
  // accepted ones out of the next list and answers the declines' reasons.
  // Sending only one family would have it re-propose the other's accepts.
  const revise = () => {
    const decisions: DoctorDecision[] = []
    for (const p of cards ?? []) {
      const v = verdicts[cardKey(p.id)]
      if (v === 'accepted' || v === 'declined') {
        decisions.push({
          proposal_id: p.id,
          field: p.character + ':' + p.field,
          rationale: p.rationale,
          accepted: v === 'accepted',
          reason: v === 'declined' ? reasons[cardKey(p.id)] || undefined : undefined,
        })
      }
    }
    for (const p of world) {
      const v = verdicts[worldKey(p.id)]
      if (v === 'accepted' || v === 'declined') {
        decisions.push({
          proposal_id: p.id,
          field: p.kind + (p.name ? ':' + p.name : p.character ? ':' + p.character : ''),
          rationale: p.rationale,
          accepted: v === 'accepted',
          reason: v === 'declined' ? reasons[worldKey(p.id)] || undefined : undefined,
        })
      }
    }
    run(decisions)
  }

  const kindLabel = (k: SessionProposal['kind']) =>
    k === 'lore_entry' ? t('📖 lore') : k === 'open_thread' ? t('🧵 thread') : k === 'lore_retire' ? t('🗑 retire') : t('✨ new character')

  const draftLabel = (f: keyof NewDraft) =>
    f === 'character' ? t('character') : f === 'description' ? t('description') : f === 'personality' ? t('personality') : t('first mes')

  // The decline affordance is identical for both families, so it is written
  // once and takes the key.
  const declineBlock = (key: string) =>
    declining[key] &&
    !verdicts[key] && (
      <div class="stage-doctor__reason">
        <input
          placeholder={t('Why not? The doctor honors the reason next round.')}
          value={reasons[key] ?? ''}
          onInput={(e) => setReasons((m) => ({ ...m, [key]: (e.target as HTMLInputElement).value }))}
        />
        <button
          onClick={() => {
            setVerdict(key, 'declined')
            setDeclining((m) => ({ ...m, [key]: false }))
          }}
        >
          {t('Decline')}
        </button>
      </div>
    )

  const verdictChip = (key: string) => (
    <>
      {verdicts[key] === 'accepted' && <span class="stage-doctor__done">{t('✓ applied')}</span>}
      {verdicts[key] === 'declined' && <span class="stage-doctor__done">{t('✗ declined')}</span>}
    </>
  )

  const decided = [...(cards ?? []).map((p) => cardKey(p.id)), ...world.map((p) => worldKey(p.id))].filter((k) =>
    ['accepted', 'declined'].includes(verdicts[k] ?? ''),
  ).length
  const total = (cards?.length ?? 0) + world.length

  const steerBox = (
    <label class="stage-doctor__steerbox">
      <span>{t('What do you want from this pass?')}</span>
      <textarea
        rows={2}
        placeholder={t('e.g. give her a rival and a mentor — or leave blank to let the doctor read')}
        value={steer}
        onInput={(e) => setSteer((e.target as HTMLTextAreaElement).value)}
      />
    </label>
  )

  const picker = (
    <ModelPick
      client={client}
      sessionId=""
      currentProvider={ovProvider}
      currentModel={ovModel}
      onSelect={(p, m) => {
        setOvProvider(p)
        setOvModel(m)
      }}
      defaultLabel={t('Default model')}
      defaultProvider={props.defaultProvider}
      defaultModel={props.defaultModel}
    />
  )

  // Which scenes inform the run. Offered rather than assumed: a World's evidence
  // is its whole history of play, and a run aimed at "what has this World
  // established that the lorebook never recorded" wants the nights that
  // established it — not whichever chat happened to be open.
  const scenePicker = props.scenes.length > 0 && (
    <div class="stage-doctor__scenes">
      <span class="stage-doctor__sceneslabel">{t('Read these scenes as evidence')}</span>
      {props.scenes.map((sc) => (
        <label key={sc.id} class="stage-doctor__scene">
          <input
            type="checkbox"
            checked={isPicked(sc.id)}
            onChange={(e) => togglePicked(sc.id, (e.target as HTMLInputElement).checked)}
          />
          <span>{sc.title || t('Untitled')}</span>
        </label>
      ))}
    </div>
  )

  return (
    <section class="stage-doctor">
      <p class="stage-hint">
        {t('Dramaturgi reads the whole cast together with this World’s lore and proposes what the ensemble needs — a character sharpened against the one they play beside, lore worth recording, and the character the cast is missing. Nothing applies unless you accept it, and an accepted character edit becomes this World’s own version rather than changing that character everywhere. One bounded model call per run.')}
      </p>
      {steerBox}
      {scenePicker}
      {picker}
      {cards === null ? (
        <button class="stage-doctor__run" disabled={running} onClick={() => run()}>
          {running ? t('Reading the World…') : t('🩺 Doctor this world')}
        </button>
      ) : (
        <>
          {note && <p class="stage-doctor__note">{note}</p>}
          {total === 0 && !note && <p class="stage-hint">{t('Nothing to propose — the cast holds together.')}</p>}

          {(cards?.length ?? 0) > 0 && <h5 class="stage-doctor__family">{t('The cast')}</h5>}
          {/* Grouped by character: at World volume a flat list makes the reader
              re-establish who each proposal is about on every row. */}
          {groupByCharacter(cards ?? []).map(([character, items]) => (
            <div key={character} class="stage-doctor__group">
              <h6 class="stage-doctor__who">{character}</h6>
              <ul class="stage-doctor__list">
                {items.map((p) => (
                  <li key={p.id} class={`stage-doctor__item stage-doctor__item--${verdicts[cardKey(p.id)] ?? 'open'}`}>
                    <div class="stage-doctor__head">
                      <span class="stage-doctor__kind">{p.field}</span>
                      {p.severity && <span class="stage-doctor__scope">{p.severity}</span>}
                      {verdictChip(cardKey(p.id))}
                    </div>
                    <p class="stage-doctor__rationale">{p.rationale}</p>
                    {/* Before and after, both shown. An accept REPLACES the
                        field, so the text about to be lost is part of the
                        decision — and `remove` is a real proposal whose "after"
                        is legitimately empty, which a bare box cannot say. */}
                    <p class="stage-doctor__before">{p.before || t('(empty)')}</p>
                    <p class="stage-doctor__content">{p.remove ? t('(cleared)') : p.after}</p>
                    {!verdicts[cardKey(p.id)] && (
                      <div class="stage-doctor__acts">
                        <button class="stage-doctor__accept" onClick={() => void acceptCard(p)}>
                          {t('Apply')}
                        </button>
                        <button class="stage-doctor__decline" onClick={() => setDeclining((m) => ({ ...m, [cardKey(p.id)]: !m[cardKey(p.id)] }))}>
                          {t('Decline…')}
                        </button>
                      </div>
                    )}
                    {declineBlock(cardKey(p.id))}
                  </li>
                ))}
              </ul>
            </div>
          ))}

          {world.length > 0 && <h5 class="stage-doctor__family">{t('The world')}</h5>}
          <ul class="stage-doctor__list">
            {world.map((p) => (
              <li key={p.id} class={`stage-doctor__item stage-doctor__item--${verdicts[worldKey(p.id)] ?? 'open'}`}>
                <div class="stage-doctor__head">
                  <span class="stage-doctor__kind">{kindLabel(p.kind)}</span>
                  <strong>{p.kind === 'character_new' ? p.character : p.name}</strong>
                  {p.kind === 'lore_retire' ? (
                    <span class="stage-doctor__scope">{t('removes this entry')}</span>
                  ) : p.kind === 'character_new' ? (
                    <span class="stage-doctor__scope">{t('joins the cast')}</span>
                  ) : (
                    <span class="stage-doctor__scope">{p.audience && p.audience.length > 0 ? `🔒 ${p.audience.join(', ')}` : t('shared')}</span>
                  )}
                  {verdictChip(worldKey(p.id))}
                </div>
                <p class="stage-doctor__rationale">{p.rationale}</p>
                {p.kind !== 'character_new' && <p class="stage-doctor__content">{p.content}</p>}
                {p.kind !== 'character_new' && p.keys && p.keys.length > 0 && <p class="stage-doctor__keys">{t('keys: %s', p.keys.join(', '))}</p>}
                {p.kind === 'character_new' && !verdicts[worldKey(p.id)] && drafts[p.id] && (
                  <div class="stage-doctor__promo">
                    {(['character', 'description', 'personality', 'first_mes'] as const).map((f) => (
                      <label key={f} class="stage-doctor__field">
                        <span>{draftLabel(f)}</span>
                        {/* Sized to the field, not uniformly. cast_promotion
                            gets away with two rows because its text is drawn
                            from lines already played; an invented character's
                            description and opening line are longer, and at two
                            rows both clipped mid-word — leaving the author
                            scrolling a small box to read the thing they are
                            being asked to approve. */}
                        <textarea
                          rows={f === 'character' ? 1 : f === 'description' ? 4 : 3}
                          value={drafts[p.id][f]}
                          onInput={(e) => setDrafts((m) => ({ ...m, [p.id]: { ...m[p.id], [f]: (e.target as HTMLTextAreaElement).value } }))}
                        />
                      </label>
                    ))}
                  </div>
                )}
                {!verdicts[worldKey(p.id)] && (
                  <div class="stage-doctor__acts">
                    {p.kind === 'character_new' ? (
                      <button class="stage-doctor__accept" onClick={() => acceptNew(p)}>
                        {t('Add to library & stage')}
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
                    <button class="stage-doctor__decline" onClick={() => setDeclining((m) => ({ ...m, [worldKey(p.id)]: !m[worldKey(p.id)] }))}>
                      {t('Decline…')}
                    </button>
                  </div>
                )}
                {declineBlock(worldKey(p.id))}
              </li>
            ))}
          </ul>

          <div class="stage-doctor__round">
            <button class="stage-doctor__revise" disabled={running} onClick={revise} title={t('Send your decisions back for a revised round')}>
              {running ? t('Reading the World…') : decided > 0 ? t('Revise with my decisions') : t('Ask again')}
            </button>
            <button class="stage-doctor__close" onClick={() => setCards(null)}>
              {t('Done')}
            </button>
          </div>
          {/* An accept mutates this SESSION's copy of the World. Saying so here
              is not a nicety: the copy-not-live-link rule is invisible from the
              seat, and an author who closes the drawer believing the World was
              updated loses the work at the next fresh scene. */}
          <p class="stage-hint">{t('Accepted changes live in this scene. Save the World to keep them for the next one.')}</p>
        </>
      )}
      {error && <p class="stage-doctor__error">{error}</p>}
    </section>
  )
}

// groupByCharacter keeps the doctor's order — the first mention of a character
// fixes their position — so a re-read after a revise does not reshuffle the
// list under someone mid-decision. Exported for its own test.
export function groupByCharacter(items: WorldCardProposal[]): [string, WorldCardProposal[]][] {
  const out: [string, WorldCardProposal[]][] = []
  const at = new Map<string, WorldCardProposal[]>()
  for (const p of items) {
    const who = p.character || p.card
    let bucket = at.get(who)
    if (!bucket) {
      bucket = []
      at.set(who, bucket)
      out.push([who, bucket])
    }
    bucket.push(p)
  }
  return out
}
