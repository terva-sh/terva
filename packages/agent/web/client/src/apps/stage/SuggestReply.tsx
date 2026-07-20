import { useEffect, useState } from 'preact/hooks'
import type { Client } from '../../platform/ctrlproto/client'
import type { SuggestTurn, SuggestResult } from '../../platform/ctrlproto/types'
import { useAutoGrow } from './autogrow'
import { ModelPick } from './ModelPick'

// SuggestReply is Stage's agentic "suggest a reply" modal — the composer aid the
// user asked for (S9): NOT SillyTavern's one-shot text-box fill, but a dedicated
// back-and-forth. You optionally sketch what you want, the model drafts it, and
// you refine it in a conversation ("shorter", "mention the door") until it's
// right.
//
// It also drives directed authorship (Phase 6): a Target picks WHOSE line this
// drafts — you (the player), a Character you bring into the scene, or the
// Narrator. A "Me" draft fills the composer for you to send; a Character/Narrator
// draft is POSTED into the transcript as an attributed line (post.line). Either
// way you approve/edit before anything happens; the model is never let to speak
// for you unbidden.
//
// The fourth target, Direct (Phase 6b), is the other disposition: instead of
// writing a line, you write an out-of-character DIRECTION ("Kael storms out")
// and the model writes the next beat following it (direct.turn) — a one-turn
// steer, not a posted line, so it skips the draft thread.
//
// Statelessly driven: the daemon holds no per-request state, so we carry the
// whole (note → draft) history on every suggest.reply and re-send it. `rounds`
// is that history; the last round's draft is the current, editable draft.
type Target = 'user' | 'actor' | 'narrator' | 'direct'

export function SuggestReply(props: {
  client: Client
  sessionId: string
  initialNote?: string
  // The session roster (Worlds W2): characters kept on stage (name → card ref),
  // offered as quick-picks in the Character target.
  roster?: Record<string, string>
  // The session's own model, so the per-generation override's "Session model"
  // row can name what inheriting actually gets you rather than just asserting
  // that it inherits.
  defaultProvider?: string
  defaultModel?: string
  onClose: () => void
  onUse: (text: string) => void
}) {
  const { client, sessionId, onClose, onUse } = props
  const roster = props.roster ?? {}
  const [rounds, setRounds] = useState<SuggestTurn[]>([])
  // Seed the sketch with whatever the user had already typed in the composer, so
  // opening ✨ mid-thought carries it in rather than throwing it away.
  const [note, setNote] = useState(props.initialNote?.trim() ?? '')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  // A per-generation model override (Phase 7): empty = the session's own model.
  const [ovProvider, setOvProvider] = useState('')
  const [ovModel, setOvModel] = useState('')
  // Directed authorship (Phase 6): whose line to draft, and — for a Character —
  // who they are (a walk-on need not be in the cast).
  const [target, setTarget] = useState<Target>('user')
  const [actorName, setActorName] = useState('')
  const [actorVoice, setActorVoice] = useState('')
  // Worlds W1: a Character can be picked from the library instead of typed. The
  // picked card ref voices them from their full card; '' = a typed walk-on.
  const [cards, setCards] = useState<{ id: string; name: string }[]>([])
  const [actorCard, setActorCard] = useState('')
  useEffect(() => {
    client
      .send<{ cards?: { id: string; name: string }[] }>('cards.list', {})
      .then((r) => setCards(r.cards ?? []))
      .catch(() => {})
  }, [])

  const last = rounds.length - 1
  const draft = last >= 0 ? rounds[last].draft : ''
  // Both inputs grow with their content (up to their CSS caps). One note box is
  // mounted at a time (sketch or refine), so a single ref serves both.
  const noteRef = useAutoGrow(note)
  const draftRef = useAutoGrow(draft)

  // A draft written in one voice doesn't carry to another, so switching the
  // target starts a fresh thread.
  const switchTarget = (t: Target) => {
    if (t === target) return
    setTarget(t)
    setRounds([])
    setError('')
  }
  // An actor draft needs SOMEONE to voice — a picked card or a typed name; "Me"
  // and the narrator never do.
  const needsName = target === 'actor' && !actorCard && !actorName.trim()

  // Run one drafting round: send the history so far plus this guidance and the
  // target, append the returned draft as the new latest round. `history` lets a
  // revert/redo re-run from an earlier point rather than the current tail.
  const run = async (guidance: string, history: SuggestTurn[]) => {
    if (needsName) return
    setBusy(true)
    setError('')
    try {
      const res = await client.send<SuggestResult>(
        'suggest.reply',
        {
          history,
          note: guidance,
          provider: ovProvider,
          model: ovModel,
          target,
          target_name: target === 'actor' ? actorName.trim() : '',
          target_voice: target === 'actor' && !actorCard ? actorVoice.trim() : '',
          target_card: target === 'actor' ? actorCard : '',
        },
        sessionId,
      )
      setRounds([...history, { note: guidance, draft: res.draft ?? '' }])
      setNote('')
    } catch (e) {
      setError(String(e))
    } finally {
      setBusy(false)
    }
  }

  // Post an approved Character/Narrator draft INTO the transcript as an attributed
  // line (post.line). The "Me" target never calls this — it fills the composer.
  const post = async () => {
    if (!draft.trim()) return
    setBusy(true)
    setError('')
    try {
      await client.send('post.line', { actor: target === 'actor' ? actorName.trim() : '', text: draft }, sessionId)
      onClose()
    } catch (e) {
      setError(String(e))
      setBusy(false)
    }
  }

  // Keep-on-stage (Worlds W2): add the picked library character to the session
  // roster so it persists and is a one-tap quick-pick next time. Chat rosters warm
  // no agents — the character is voiced on demand via this modal.
  const inRoster = !!actorCard && Object.values(roster).includes(actorCard)
  const keepOnStage = async () => {
    if (!actorCard || !actorName.trim()) return
    setError('')
    try {
      await client.send('cast.add', { name: actorName.trim(), ref: actorCard }, sessionId)
    } catch (e) {
      setError(String(e))
    }
  }

  // Direct (Phase 6b): run one turn steered by the typed direction — the model
  // writes the beat. No draft thread; the note IS the direction.
  const isDirect = target === 'direct'
  const directStory = async () => {
    const text = note.trim()
    if (!text) return
    setBusy(true)
    setError('')
    try {
      await client.send('direct.turn', { text }, sessionId)
      onClose()
    } catch (e) {
      setError(String(e))
      setBusy(false)
    }
  }

  // Hand-edits to the current draft feed back into the refinement: the next
  // "refine" call threads the edited text, not the model's original.
  const editDraft = (text: string) => {
    if (last < 0) return
    setRounds((rs) => rs.map((r, i) => (i === last ? { ...r, draft: text } : r)))
  }

  // Revert to an earlier draft: drop everything after round i and make it current
  // again, so a refinement that went the wrong way can be backed out.
  const revertTo = (i: number) => setRounds((rs) => rs.slice(0, i + 1))

  // Redo the latest round with the same guidance (a fresh sample), keeping the
  // history before it intact.
  const regenerate = () => {
    if (last < 0) return
    void run(rounds[last].note ?? '', rounds.slice(0, last))
  }

  const composerHint = (n: string) => n.trim() || 'cold suggestion'

  const heading =
    target === 'user'
      ? '✨ Suggest a reply'
      : target === 'narrator'
        ? '🎭 Narrate a beat'
        : target === 'direct'
          ? '🎬 Direct the story'
          : '🎭 Bring in a character'
  const sketchHint =
    target === 'user'
      ? "Sketch what you want to say — a few words, the gist, or leave it blank and I'll suggest something in your voice."
      : target === 'narrator'
        ? "Sketch the beat — a scene change, an entrance, a shift in mood — or leave it blank and I'll write one."
        : `Sketch what ${actorName.trim() || 'they'} says or does — or leave it blank and I'll voice them.`
  const sketchPlaceholder =
    target === 'user' ? 'e.g. push back, but stay flirty…' : target === 'narrator' ? 'e.g. night falls; a stranger slips in…' : 'e.g. warns them off, then softens…'

  return (
    <div class="stage-sheet-backdrop" onClick={onClose}>
      <div class="stage-suggest" onClick={(e) => e.stopPropagation()}>
        <header class="stage-suggest__head">
          <h3>{heading}</h3>
          <button class="stage-drawer__close" onClick={onClose}>
            ✕
          </button>
        </header>

        <div class="stage-suggest__target" role="group" aria-label="Who speaks">
          <button class={target === 'user' ? 'is-active' : ''} disabled={busy} onClick={() => switchTarget('user')}>
            Me
          </button>
          <button class={target === 'actor' ? 'is-active' : ''} disabled={busy} onClick={() => switchTarget('actor')}>
            🎭 Character
          </button>
          <button class={target === 'narrator' ? 'is-active' : ''} disabled={busy} onClick={() => switchTarget('narrator')}>
            ✍ Narrator
          </button>
          <button class={target === 'direct' ? 'is-active' : ''} disabled={busy} onClick={() => switchTarget('direct')}>
            🎬 Direct
          </button>
        </div>

        {target === 'actor' && Object.keys(roster).length > 0 && (
          <div class="stage-suggest__roster">
            <span class="stage-suggest__roster-label">On stage</span>
            {Object.entries(roster).map(([name, ref]) => (
              <button
                key={ref}
                class={`stage-suggest__roster-chip${actorCard === ref ? ' is-active' : ''}`}
                disabled={busy}
                onClick={() => {
                  setActorCard(ref)
                  setActorName(name)
                }}
              >
                🎭 {name}
              </button>
            ))}
          </div>
        )}

        {target === 'actor' && (
          <div class="stage-suggest__voice">
            {/* Worlds W1: pick a character from the library to voice them from
                their full card, or fall back to a typed walk-on. */}
            {cards.length > 0 && (
              <select
                class="stage-suggest__card-pick"
                value={actorCard}
                disabled={busy}
                onChange={(e) => {
                  const ref = (e.target as HTMLSelectElement).value
                  setActorCard(ref)
                  const c = cards.find((x) => x.id === ref)
                  setActorName(c ? c.name : '') // card name, or clear for a fresh walk-on
                }}
              >
                <option value="">Walk-on (type a character)…</option>
                {cards.map((c) => (
                  <option key={c.id} value={c.id}>
                    {c.name}
                  </option>
                ))}
              </select>
            )}
            {actorCard ? (
              <div class="stage-suggest__card-chosen">
                <p class="stage-suggest__card-note">Voicing {actorName} from their card.</p>
                {inRoster ? (
                  <span class="stage-suggest__card-note">On stage.</span>
                ) : (
                  <button class="stage-suggest__keep" disabled={busy} title="Keep this character on stage for the scene" onClick={() => void keepOnStage()}>
                    ＋ Keep on stage
                  </button>
                )}
              </div>
            ) : (
              <>
                <input
                  class="stage-suggest__actor-name"
                  value={actorName}
                  placeholder="Character name (e.g. Kael)"
                  disabled={busy}
                  onInput={(e) => setActorName((e.target as HTMLInputElement).value)}
                />
                <input
                  class="stage-suggest__actor-voice"
                  value={actorVoice}
                  placeholder="Who are they? (optional — a gruff dockmaster)"
                  disabled={busy}
                  onInput={(e) => setActorVoice((e.target as HTMLInputElement).value)}
                />
              </>
            )}
          </div>
        )}

        {/* Direct runs a full session turn (direct.turn), not a one-shot draft, so
            the per-generation model override doesn't apply — hide the picker. */}
        {!isDirect && (
          <div class="stage-suggest__model">
            <ModelPick
              client={client}
              sessionId={sessionId}
              currentProvider={ovProvider}
              currentModel={ovModel}
              onSelect={(p, m) => {
                setOvProvider(p)
                setOvModel(m)
              }}
              defaultLabel="Session model"
              defaultProvider={props.defaultProvider}
              defaultModel={props.defaultModel}
            />
          </div>
        )}

        {error && (
          <p class="stage-error" onClick={() => setError('')}>
            {error}
          </p>
        )}

        {isDirect ? (
          <div class="stage-suggest__sketch">
            <p class="stage-suggest__hint">
              Tell the story what happens next — the narrator writes it. Your direction steers one turn; it isn't spoken as your character.
            </p>
            <textarea
              ref={noteRef}
              class="stage-suggest__note"
              value={note}
              placeholder="e.g. Kael refuses the offer and storms out."
              rows={3}
              autofocus
              disabled={busy}
              onInput={(e) => setNote((e.target as HTMLTextAreaElement).value)}
              onKeyDown={(e) => {
                if (e.key === 'Enter' && (e.metaKey || e.ctrlKey)) {
                  e.preventDefault()
                  if (!busy && note.trim()) void directStory()
                }
              }}
            />
            <div class="stage-suggest__sketch-actions">
              <button class="stage-suggest__go" disabled={busy || !note.trim()} onClick={() => void directStory()}>
                {busy ? 'Directing…' : 'Direct the story →'}
              </button>
            </div>
          </div>
        ) : rounds.length === 0 ? (
          <div class="stage-suggest__sketch">
            <p class="stage-suggest__hint">{sketchHint}</p>
            <textarea
              ref={noteRef}
              class="stage-suggest__note"
              value={note}
              placeholder={sketchPlaceholder}
              rows={3}
              autofocus
              disabled={busy}
              onInput={(e) => setNote((e.target as HTMLTextAreaElement).value)}
              onKeyDown={(e) => {
                if (e.key === 'Enter' && (e.metaKey || e.ctrlKey)) {
                  e.preventDefault()
                  if (!busy && !needsName) void run(note, [])
                }
              }}
            />
            <div class="stage-suggest__sketch-actions">
              <button
                class="stage-suggest__go"
                disabled={busy || needsName}
                title={needsName ? 'Name the character first' : ''}
                onClick={() => void run(note, [])}
              >
                {busy ? 'Drafting…' : 'Draft it ✨'}
              </button>
            </div>
          </div>
        ) : (
          <>
            <div class="stage-suggest__thread">
              {rounds.map((r, i) => (
                <div key={i} class="stage-suggest__round">
                  <div class={`stage-suggest__guide ${r.note?.trim() ? '' : 'stage-suggest__guide--cold'}`}>
                    {r.note?.trim() ? `“${r.note.trim()}”` : 'cold suggestion'}
                  </div>
                  {i === last ? (
                    <textarea
                      ref={draftRef}
                      class="stage-suggest__draft"
                      value={draft}
                      rows={4}
                      disabled={busy}
                      onInput={(e) => editDraft((e.target as HTMLTextAreaElement).value)}
                    />
                  ) : (
                    <div class="stage-suggest__draft-past">
                      <div class="stage-suggest__draft-text">{r.draft}</div>
                      <button class="stage-suggest__revert" title="Go back to this draft" onClick={() => revertTo(i)}>
                        ↩ revert
                      </button>
                    </div>
                  )}
                </div>
              ))}
            </div>

            <div class="stage-suggest__refine">
              <textarea
                ref={noteRef}
                class="stage-suggest__note"
                value={note}
                placeholder="Refine it — “shorter”, “angrier”, “mention the locked door”…"
                rows={2}
                disabled={busy}
                onInput={(e) => setNote((e.target as HTMLTextAreaElement).value)}
                onKeyDown={(e) => {
                  if (e.key === 'Enter' && (e.metaKey || e.ctrlKey)) {
                    e.preventDefault()
                    if (!busy && note.trim()) void run(note, rounds)
                  }
                }}
              />
              <div class="stage-suggest__refine-actions">
                <button
                  class="stage-suggest__refine-go"
                  disabled={busy || !note.trim()}
                  title="Refine the draft with this note"
                  onClick={() => void run(note, rounds)}
                >
                  {busy ? '…' : 'Refine'}
                </button>
                <button class="stage-suggest__regen" disabled={busy} title="Try the last note again" onClick={regenerate}>
                  ↻
                </button>
              </div>
            </div>

            <footer class="stage-suggest__foot">
              <button class="stage-suggest__cancel" onClick={onClose}>
                Cancel
              </button>
              {target === 'user' ? (
                <button
                  class="stage-suggest__use"
                  disabled={busy || !draft.trim()}
                  title={composerHint(note)}
                  onClick={() => {
                    onUse(draft)
                    onClose()
                  }}
                >
                  Use this →
                </button>
              ) : (
                <button
                  class="stage-suggest__use"
                  disabled={busy || !draft.trim()}
                  title={`Post this ${target === 'narrator' ? 'beat' : 'line'} into the scene`}
                  onClick={() => void post()}
                >
                  Post to scene →
                </button>
              )}
            </footer>
          </>
        )}
      </div>
    </div>
  )
}
