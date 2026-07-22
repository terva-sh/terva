import { useCallback, useEffect, useLayoutEffect, useRef, useState } from 'preact/hooks'
import type { ClientLike } from '../../platform/ctrlproto/client'
import type { AskRequest, CardView, CreateOpts, SessionInfo } from '../../platform/ctrlproto/types'
import type { Item } from '../../platform/conversation/store'
import { t, tn } from '../../i18n'
import { panelHref } from '../../ui/navlinks'
import { handleCodeCopyClick } from '../../ui/codecopy'
import { truncate } from '../../ui/formatting'
import { ImageGallery } from '../../ui/ImageGallery'
import { Markdown } from '../../ui/Markdown'
import { useConversation } from './useConversation'
import { useAutoGrow } from './autogrow'
import { ModelPick } from './ModelPick'
import { Steering } from './Steering'
import { SuggestReply } from './SuggestReply'
import { RealizeSheet } from './RealizeSheet'
import { CREATOR_PERSONA } from './creator'
import { DoctorOverlay, type DoctorAsk } from './SessionDoctor'
import { SceneStateCard, sceneStateOf } from './SceneState'
import { NextSceneSheet } from './NextScene'
import { CardSheet } from './CardSheet'
import { CardEditor } from './CardEditor'

type Character = { name: string; avatar?: string }

// The immersive chat screen (3c): a full-bleed scene background, avatar-anchored
// rows with roleplay markdown, swipe arrows + regenerate on the last response
// (the Phase-1 tail primitives), and tap-to-edit any message. It renders over the
// shared conversation store via useConversation.
// BRANCH_HINT: an edit this many messages above the end is "deep" — its downstream
// was written to the original, so the edit box points out that branching keeps them.
const BRANCH_HINT = 2

// deleteWarning is the confirm text for removing a message. Deleting the last
// message is a clean rewind; deleting one with a downstream is not, because those
// replies were written to the message that is about to vanish and they stay. The
// prompt has to say so — "This can't be undone" is true of both and distinguishes
// nothing, and the scene reading crooked afterwards is the surprise worth spending
// a sentence on. Exported for its own test: the arithmetic decides which of two
// materially different things the user is agreeing to.
export function deleteWarning(downstream: number): string {
  if (downstream <= 0) return t('Delete this message? This can’t be undone.')
  return tn(
    downstream,
    'Delete this message? The %d reply was written to it and will stay, so the scene may not read straight afterwards. Branch instead to keep this thread intact.',
    'Delete this message? The %d replies were written to it and will stay, so the scene may not read straight afterwards. Branch instead to keep this thread intact.',
  )
}

export function Chat(props: {
  client: ClientLike
  sessionId: string
  // Connection generation — bumped on every (re)connect so the subscription is
  // re-established. See useConversation.
  generation?: number
  onBack: () => void
  onOpenSession: (session: string) => void
}) {
  const { client, sessionId, onOpenSession } = props
  const { items, busy, info, tail, msgMarks, permission, ask, send, edit, deleteAt, swipe, swipeAt, pruneAt, dropAt, retry, continueTurn, advance, cancel, decide, answerAsk, fork, discardDraft } = useConversation(client, sessionId, props.generation)
  const [draft, setDraft] = useState('')
  const [character, setCharacter] = useState<Character | null>(null)
  // The full bound card, kept so the header can open its detail/edit sheets
  // without a round-trip to the Library. cardSheet = the view; cardEditing = the
  // editor reached from it (same view→edit hand-off the Library grid uses).
  const [cardView, setCardView] = useState<CardView | null>(null)
  const [cardSheet, setCardSheet] = useState(false)
  const [cardEditing, setCardEditing] = useState(false)
  const [editing, setEditing] = useState<{ idx: number; text: string } | null>(null)
  // The guided-regenerate box on the last response: null is closed, and ↻ stays a
  // one-click plain regenerate whether it is open or not. ignorePrior is per
  // regeneration rather than a remembered setting — "don't look at that one" is a
  // judgement about the take in front of you, not a standing preference.
  const [guiding, setGuiding] = useState<{ text: string; ignorePrior: boolean } | null>(null)
  const [steerOpen, setSteerOpen] = useState(false)
  const [suggestOpen, setSuggestOpen] = useState(false)
  // The realize review sheet (creator C3): open from a creator conversation to
  // turn it into a playable world.
  const [realizeOpen, setRealizeOpen] = useState(false)
  // A narrowed session-doctor ask (SD2/SD3), launched from a message's edit
  // actions: keep one moment as lore, or promote a walk-on the scene voiced.
  const [doctorAsk, setDoctorAsk] = useState<DoctorAsk | null>(null)
  // The scene-break flow (SD5): null = closed, a string = open with that
  // title seeded (empty when the author started it themselves).
  const [nextScene, setNextScene] = useState<string | null>(null)
  const [error, setError] = useState('')
  // The composer grows with its content (up to the CSS cap); keyed on `draft` so
  // it also fits text dropped in by ✨ Suggest and shrinks back after a send.
  const composerRef = useAutoGrow(draft)

  // Resolve the character from the session's card, for the avatar-anchored rows,
  // the header, and the card sheets it opens. Kept as a callback so an edit that
  // saves the card can refresh it in place (the card ref never changes, so the
  // effect below won't refire on its own).
  const loadCard = useCallback(() => {
    if (!info?.card) return
    client
      .send<CardView>('cards.get', { id: info.card })
      .then((c) => {
        setCharacter({ name: c.name, avatar: c.avatar_url })
        setCardView(c)
      })
      .catch(() => {})
  }, [client, info?.card])
  useEffect(() => {
    loadCard()
  }, [loadCard])

  // Stick to the newest message: a chat lands at the bottom (where the composer
  // is) on load, and follows new turns as they stream — unless the reader has
  // scrolled up into history, in which case we leave them where they are.
  const transcriptRef = useRef<HTMLElement>(null)
  const stick = useRef(true)
  // Whether to offer a "↓ jump to latest" button — shown only once the reader has
  // scrolled up off the newest line (mirrors the panel's ConversationTimeline).
  const [showJump, setShowJump] = useState(false)
  useEffect(() => {
    stick.current = true // a freshly opened session starts pinned to its end
    setShowJump(false)
  }, [sessionId])
  useLayoutEffect(() => {
    const el = transcriptRef.current
    if (el && stick.current) el.scrollTop = el.scrollHeight
  }, [items, sessionId])
  const onTranscriptScroll = () => {
    const el = transcriptRef.current
    if (!el) return
    const nearBottom = el.scrollHeight - el.scrollTop - el.clientHeight < 80
    stick.current = nearBottom
    setShowJump(!nearBottom)
  }
  const jumpToLatest = () => {
    const el = transcriptRef.current
    if (!el) return
    el.scrollTop = el.scrollHeight
    stick.current = true
    setShowJump(false)
  }

  const bg = info?.background ? `/media/backgrounds/${info.background}` : ''
  // A creator session (C1) is a card-less chat with the cartographer: no card to
  // name it, and an empty transcript that must prompt rather than sit blank.
  const isCreator = info?.persona === CREATOR_PERSONA && !info?.card
  // A set session title wins over the card name (matching the Library's
  // `title || name`), so a rename or ✨-regenerate from Steering shows here; an
  // untitled chat falls back to the character it stars, or — with no card — to a
  // workshop label for the creator.
  const title = info?.title || character?.name || (isCreator ? t('Workshop') : t('Chat'))
  const lastAssistantId = [...items].reverse().find((it) => it.kind === 'assistant')?.id

  const submit = () => {
    const t = draft.trim()
    if (!t || busy) return
    send(t)
    setDraft('')
  }
  const guard = (p: Promise<unknown>) => p.catch((e: unknown) => setError(String(e)))
  // User-directs: pick who speaks. The narrator is directed to bring the actor
  // into the scene (a normal turn), so it reuses the transcript + attribution.
  const castSpeak = (actor: string) => guard(client.send('cast.speak', { actor }, sessionId))
  const cast = info?.experience === 'play' ? info?.cast ?? {} : {}
  const sceneState = sceneStateOf(info?.world_lore)
  const saveEdit = () => {
    if (!editing) return
    guard(edit(editing.idx, editing.text))
    setEditing(null)
  }
  // Submitting an empty box is not an error — it is the plain regenerate, which is
  // what someone who opened the box and then had nothing to add actually wants.
  // retry() drops the field when the text is blank, so the daemon sees the
  // untouched original verb.
  const submitGuided = () => {
    if (!guiding) return
    guard(retry(guiding.text, guiding.ignorePrior))
    setGuiding(null)
  }
  // Delete a message outright. Confirms first (the Library's idiom for a
  // destructive act), and closes the edit box before the snapshot lands — the
  // indices below shift up by one, so an edit box still pointing at `idx` would
  // be aimed at a different message than the one the user opened.
  const deleteMessage = (index: number) => {
    if (!window.confirm(deleteWarning(msgCount - 1 - index))) return
    setEditing(null)
    guard(deleteAt(index))
  }
  // Branch (fork) at a message: a new session that shares the transcript through it
  // and diverges, leaving this one intact — the coherence-preserving alternative to
  // editing with a long downstream. Switch to the new branch on success.
  const branchAt = (index: number) => {
    setEditing(null)
    guard(fork(index).then((r) => onOpenSession(r.session.id)))
  }
  // The transcript's message count, for sizing the downstream of a deep edit.
  const msgCount = items.reduce((n, it) => Math.max(n, (it.idx ?? -1) + 1), 0)

  // Start a fresh chat with this same character — the card sheet's Start button,
  // reachable from inside a scene without a trip back to the Library.
  const startChatFrom = (greeting: number) => {
    if (!info?.card) return
    guard(
      client
        .send<{ session: SessionInfo }>('sessions.create', { experience: 'chat', card: info.card, greeting } as CreateOpts)
        .then((r) => onOpenSession(r.session.id)),
    )
  }

  return (
    <div class="stage stage-chat" style={bg ? { backgroundImage: `url("${bg}")` } : undefined}>
      <div class="stage-chat__scrim" />
      <header class="stage-topbar">
        <button
          class="stage-back"
          onClick={() => {
            // Leaving a preview the user never sent into: reclaim the draft (a
            // guarded no-op on a real chat) so it doesn't linger live.
            discardDraft()
            props.onBack()
          }}
        >
          {t('‹ Library')}
        </button>
        {/* The character's portrait + name open its card — view every field,
            or ✎ Edit — right from the scene, instead of backing out to the
            Library to find it. Only a card-backed session has one to open; a
            creator or plain-persona chat keeps the bare title. */}
        {info?.card && cardView ? (
          <button
            class="stage-chat__cardbtn"
            title={t('%s — view or edit their card', character?.name ?? title)}
            onClick={() => setCardSheet(true)}
          >
            {cardView.avatar_url ? (
              <img class="stage-chat__cardavatar" src={cardView.avatar_url} alt="" />
            ) : (
              <span class="stage-chat__cardavatar stage-chat__cardavatar--blank" aria-hidden="true">{initial(title)}</span>
            )}
            <span class="stage-chat__title">{title}</span>
          </button>
        ) : (
          <span class="stage-chat__title">{title}</span>
        )}
        <div class="stage-chat__right">
          <span class="stage-status">{busy ? t('thinking…') : ''}</span>
          {/* The model, up front: the config reached for most often in a quick
              testing loop, now zero clicks to read and one to switch — the same
              live models.switch the Steering drawer fires, surfaced here. */}
          <ModelPick client={client} sessionId={sessionId} currentProvider={info?.provider} currentModel={info?.model} compact />
          {/* Carries the session, so the panel lands on THIS chat rather than
              whichever one it considers current. */}
          <a
            class="stage-nav-link stage-nav-link--icon"
            href={panelHref(sessionId)}
            title={t('Open this session in the control panel')}
            aria-label={t('Open this session in the control panel')}
          >
            ⌂
          </a>
          <button class="stage-steer-btn" title={t('Steering')} onClick={() => setSteerOpen(true)}>☰</button>
        </div>
      </header>

      <main class="stage-transcript" ref={transcriptRef} onScroll={onTranscriptScroll}>
        {items.length === 0 &&
          (isCreator ? (
            // The creator must not speak first (an ungrounded greeting is its most
            // ungrounded output), so the blank page prompts and grounds instead —
            // static copy, not a model turn (creator C1, Finding 1).
            <div class="stage-creator-empty">
              <div class="stage-creator-empty__icon" aria-hidden="true">🧭</div>
              <p class="stage-creator-empty__lead">{t('Tell the cartographer what you have.')}</p>
              <p class="stage-creator-empty__hint">
                {t('A spark, a paragraph, a character you can already picture — or paste something you wrote. It draws your seed a few genuinely different ways, each one you could start playing tonight, and you keep the one that was already yours.')}
              </p>
            </div>
          ) : (
            <p class="stage-empty">{t('Say something to begin.')}</p>
          ))}
        {items.map((it) => (
          <ChatRow
            key={it.id}
            item={it}
            character={character}
            editing={editing != null && editing.idx === it.idx}
            editText={editing?.text ?? ''}
            onEditText={(t) => setEditing((e) => (e ? { ...e, text: t } : e))}
            onStartEdit={() => it.idx != null && 'text' in it && setEditing({ idx: it.idx, text: it.text })}
            onSaveEdit={saveEdit}
            onCancelEdit={() => setEditing(null)}
            onBranch={() => it.idx != null && branchAt(it.idx)}
            onDelete={() => it.idx != null && deleteMessage(it.idx)}
            onKeepAsLore={() => {
              if (it.idx == null) return
              setEditing(null)
              setDoctorAsk({ focus: it.idx })
            }}
            onPromote={
              'actor' in it && it.actor && (it.directed || it.routed) && !(it.actor in (info?.cast ?? {}))
                ? () => {
                    setEditing(null)
                    setDoctorAsk({ promote: it.actor! })
                  }
                : null
            }
            branchNote={
              editing != null && editing.idx === it.idx && it.idx != null && msgCount - 1 - it.idx >= BRANCH_HINT
                ? tn(
                    msgCount - 1 - it.idx,
                    '%d message below was written to this — Save keeps it, Branch starts a new thread.',
                    '%d messages below were written to this — Save keeps them, Branch starts a new thread.',
                  )
                : undefined
            }
            isLast={it.id === lastAssistantId}
            tail={it.id === lastAssistantId ? tail : undefined}
            mark={it.idx != null ? msgMarks.get(it.idx) : undefined}
            busy={busy}
            canContinue={info?.supports_continue ?? false}
            onSwipe={(v) => guard(swipe(v))}
            onSwipeAt={(v) => it.idx != null && guard(swipeAt(it.idx, v))}
            onPrune={() => it.idx != null && guard(pruneAt(it.idx))}
            onDropTake={(v) => it.idx != null && guard(dropAt(it.idx, v))}
            onRetry={() => guard(retry())}
            guiding={it.id === lastAssistantId ? guiding : null}
            onOpenGuide={() => {
              setEditing(null) // the edit box and the guidance box share the row
              setGuiding({ text: '', ignorePrior: false })
            }}
            onGuideChange={setGuiding}
            onSubmitGuide={submitGuided}
            onCancelGuide={() => setGuiding(null)}
            onContinue={() => guard(continueTurn())}
          />
        ))}
        {/* Sticks to the bottom of the transcript's own viewport, so it hovers
            just above the composer no matter how tall the composer grows. Only
            mounted while scrolled up, so it costs no layout at rest. */}
        {showJump && (
          <button class="stage-jump" onClick={jumpToLatest}>
            ↓ {t('Jump to latest')}
          </button>
        )}
      </main>

      {Object.keys(cast).length > 0 && (
        <div class="stage-cast-strip">
          <span class="stage-cast-strip__label">{t('Who speaks?')}</span>
          {Object.keys(cast).map((name) => (
            <button key={name} class="stage-cast-strip__actor" disabled={busy} onClick={() => castSpeak(name)}>
              🎭 {name}
            </button>
          ))}
        </div>
      )}

      {/* A turn that reaches for a gated tool, or asks a question, blocks on the
          user. Stage used to ignore both events, so the turn parked on the daemon
          with nothing on screen — it just looked like nothing happened. Rendered
          above the composer, where the answer is expected. */}
      {permission && (
        <div class="stage-interact">
          <div class="stage-interact__head">
            {t('Approve tool:')} <code>{permission.tool}</code>
          </div>
          {permission.preview && <pre class="stage-interact__preview">{truncate(permission.preview, 1500)}</pre>}
          <div class="stage-interact__actions">
            <button class="stage-interact__go" onClick={() => decide(permission.call_id, { allow: true })}>{t('Allow')}</button>
            <button title={t('For the rest of this session')} onClick={() => decide(permission.call_id, { allow: true, remember_tool: true })}>{t('Allow & remember')}</button>
            <button class="stage-interact__deny" onClick={() => decide(permission.call_id, { allow: false, reason: 'denied by user' })}>{t('Deny')}</button>
          </div>
        </div>
      )}

      {ask && <AskPrompt request={ask} onAnswer={answerAsk} />}

      {/* The pinned scene-state card (SD4): above the composer, not buried in
          lore — the one piece of World state worth a permanent slot on the
          play surface. Rendered only when a card is pinned; pinning happens
          via the doctor, the model (world_note), or Steering's World tab. */}
      {sceneState && <SceneStateCard client={client} sessionId={sessionId} entry={sceneState} stale={info?.scene_pin_stale} />}

      {/* A refused action reports NEXT TO THE CONTROL THAT WAS REFUSED, which
          means outside the transcript. This used to render as the first child of
          .stage-transcript — the very top of the scrollback — while the
          transcript pins itself to the bottom, so the daemon's answer to a ↻ at
          the end of a long scene ("nothing to retry", a stale-epoch conflict,
          "regenerating would discard the lines you wrote") scrolled hundreds of
          messages out of view the moment it appeared. The button looked dead:
          the refusal WAS rendered, just never where anyone was looking. Same
          reasoning as the working signal below — it lives out here so it cannot
          scroll away. */}
      {error && (
        <p class="stage-error stage-error--live" role="alert" onClick={() => setError('')} title={t('Dismiss')}>
          {error}
        </p>
      )}

      {/* The working signal, at the bottom where the waiting happens. The header's
          "thinking…" is easy to miss, and the per-message caret only appears once
          the FIRST token lands — so the whole provider round-trip, which is exactly
          when you wonder whether your submit registered, had nothing at all. Driven
          off busy, not off streaming, so it covers that gap; and it lives outside
          the transcript, so it does not scroll away when you read back. */}
      {busy && (
        <div class="stage-working" role="status" aria-live="polite">
          <span class="stage-working__dots" aria-hidden="true">
            <i />
            <i />
            <i />
          </span>
          <span class="stage-working__label">{t('working…')}</span>
        </div>
      )}

      {/* The exit from a creator conversation (C3): once there is something to
          work from, offer to realize it into a playable world. */}
      {isCreator && items.length > 0 && !busy && (
        <div class="stage-realize-bar">
          <span class="stage-realize-bar__label">{t('Happy with the shape of it?')}</span>
          <button class="stage-realize-bar__go" onClick={() => setRealizeOpen(true)}>
            {t('🧭 Realize this world →')}
          </button>
        </div>
      )}

      <footer class="stage-composer">
        <button
          class="stage-suggest-btn"
          title={t('Suggest a reply — draft your next message with the model')}
          aria-label={t('Suggest a reply')}
          onClick={() => setSuggestOpen(true)}
        >
          ✨
        </button>
        {/* Advance: let the scene move on without you saying anything. The other
            half of ✨ — that one drafts YOUR line, this one asks for theirs. */}
        <button
          class="stage-advance-btn"
          title={t('Advance — let the scene continue without saying anything')}
          aria-label={t('Advance the scene')}
          disabled={busy}
          onClick={() => guard(advance())}
        >
          ▶
        </button>
        <textarea
          ref={composerRef}
          value={draft}
          placeholder={t('Say something…')}
          rows={1}
          onInput={(e) => setDraft((e.target as HTMLTextAreaElement).value)}
          onKeyDown={(e) => {
            if (e.key === 'Enter' && !e.shiftKey) {
              e.preventDefault()
              submit()
            }
          }}
        />
        {/* Send becomes Stop while a turn runs (the panel's convention), so the
            interrupt is where your hands already are and costs no extra room.
            Stopping is what lets you get a word in: post.line / cast.speak / ▶ all
            reject while a turn is in flight, and a cancelled turn keeps whatever
            streamed as a normal truncated line, so the scene stays usable. */}
        {busy ? (
          <button class="stage-stop-btn" title={t('Stop — interrupt this turn so you can steer the scene')} onClick={cancel}>
            {t('■ Stop')}
          </button>
        ) : (
          <button onClick={submit} disabled={!draft.trim()}>{t('Send')}</button>
        )}
      </footer>

      {steerOpen && (
        <Steering
          client={client}
          sessionId={sessionId}
          info={info}
          onClose={() => setSteerOpen(false)}
          onNextScene={(title) => setNextScene(title)}
        />
      )}

      {doctorAsk && <DoctorOverlay client={client} sessionId={sessionId} ask={doctorAsk} defaultProvider={info?.provider} defaultModel={info?.model} onClose={() => setDoctorAsk(null)} />}

      {nextScene !== null && (
        <NextSceneSheet
          client={client}
          sessionId={sessionId}
          suggestedTitle={nextScene}
          defaultProvider={info?.provider}
          defaultModel={info?.model}
          onOpenSession={onOpenSession}
          onClose={() => setNextScene(null)}
        />
      )}

      {suggestOpen && (
        <SuggestReply
          client={client}
          sessionId={sessionId}
          initialNote={draft}
          roster={info?.cast ?? {}}
          defaultProvider={info?.provider}
          defaultModel={info?.model}
          onClose={() => setSuggestOpen(false)}
          onUse={(text) => setDraft(text)}
        />
      )}

      {realizeOpen && (
        <RealizeSheet
          client={client}
          sessionId={sessionId}
          defaultProvider={info?.provider}
          defaultModel={info?.model}
          onOpenSession={onOpenSession}
          onClose={() => setRealizeOpen(false)}
        />
      )}

      {/* The bound character's card, opened from the header portrait. The view
          sheet fetches the full card itself; a CardView already satisfies its
          CardSummary prop, so the one Chat holds seeds it with no extra fetch.
          ✎ Edit hands off to the editor, and a save refreshes the header. */}
      {cardSheet && cardView && (
        <CardSheet
          client={client}
          card={cardView}
          busy={busy}
          onClose={() => setCardSheet(false)}
          onStart={(g) => {
            setCardSheet(false)
            startChatFrom(g)
          }}
          onEdit={() => {
            setCardSheet(false)
            setCardEditing(true)
          }}
        />
      )}

      {cardEditing && cardView && (
        <CardEditor
          client={client}
          card={cardView}
          onClose={() => setCardEditing(false)}
          onSaved={() => {
            setCardEditing(false)
            loadCard()
          }}
        />
      )}
    </div>
  )
}

function initial(name?: string): string {
  return (name?.trim()?.[0] ?? '·').toUpperCase()
}

// AskPrompt renders a question the turn is blocked on: the offered options, plus
// a free-text box when the asker allows one. Its own component because the custom
// answer needs local state, which the Chat body has no business holding.
function AskPrompt(props: { request: AskRequest; onAnswer: (id: string, text: string) => void }) {
  const { request, onAnswer } = props
  const [custom, setCustom] = useState('')
  return (
    <div class="stage-interact">
      <div class="stage-interact__head">{request.question}</div>
      {(request.options ?? []).length > 0 && (
        <div class="stage-interact__actions">
          {(request.options ?? []).map((option) => (
            <button key={option} onClick={() => onAnswer(request.ask_id, option)}>
              {option}
            </button>
          ))}
        </div>
      )}
      {request.allow_custom && (
        <form
          class="stage-interact__custom"
          onSubmit={(e) => {
            e.preventDefault()
            if (custom.trim()) onAnswer(request.ask_id, custom.trim())
          }}
        >
          <input value={custom} placeholder={t('Your answer…')} onInput={(e) => setCustom((e.target as HTMLInputElement).value)} />
          <button class="stage-interact__go" type="submit" disabled={!custom.trim()}>
            {t('Answer')}
          </button>
        </form>
      )}
    </div>
  )
}

function ChatRow(props: {
  item: Item
  character: Character | null
  editing: boolean
  editText: string
  onEditText: (t: string) => void
  onStartEdit: () => void
  onSaveEdit: () => void
  onCancelEdit: () => void
  onBranch: () => void
  onDelete: () => void
  onKeepAsLore: () => void
  // null hides the button: only an attributed line whose actor is not already
  // on stage can promote (the server re-checks either way).
  onPromote: (() => void) | null
  branchNote: string | undefined
  onPrune: () => void
  onDropTake: (variant: number) => void
  isLast: boolean
  tail: { span_start: number; variants: number; active: number } | undefined
  mark: { index: number; variants: number; active: number } | undefined
  busy: boolean
  canContinue: boolean
  onSwipe: (variant: number) => void
  onSwipeAt: (variant: number) => void
  onRetry: () => void
  guiding: { text: string; ignorePrior: boolean } | null
  onOpenGuide: () => void
  onGuideChange: (g: { text: string; ignorePrior: boolean }) => void
  onSubmitGuide: () => void
  onCancelGuide: () => void
  onContinue: () => void
}) {
  const { item, character, editing, isLast, tail, mark, busy } = props

  // The edit box grows with its content up to a CSS cap (like the composer),
  // keyed on editText so it fits the whole message the instant editing opens —
  // not a 4-line slit you scroll a long entry through. The ref sits idle (null)
  // on every non-editing row, so the per-row hook is a no-op until this one edits.
  const editRef = useAutoGrow(props.editText)

  // A bubble is both "copy this code block" and "tap to edit this message", and
  // the copy button sits INSIDE the bubble — so a bare delegated listener on an
  // ancestor would fire after the bubble had already opened the editor. Try the
  // copy first and only fall through to editing when the click was not on one.
  const onBubbleClick = (event: MouseEvent) => {
    if (handleCodeCopyClick(event)) return
    props.onStartEdit()
  }

  const editBox = (
    <div class="stage-edit">
      <textarea ref={editRef} value={props.editText} onInput={(e) => props.onEditText((e.target as HTMLTextAreaElement).value)} />
      <div class="stage-edit__actions">
        <button class="stage-edit__save" onClick={props.onSaveEdit}>{t('Save')}</button>
        <button class="stage-edit__cancel" onClick={props.onCancelEdit}>{t('Cancel')}</button>
        <button class="stage-edit__branch" title={t('Start a new thread from here')} onClick={props.onBranch}>{t('⑂ Branch here')}</button>
        {mark && mark.variants > 1 && (
          <button class="stage-edit__prune" title={t("Discard this message's other versions")} onClick={props.onPrune}>{t('Keep only this')}</button>
        )}
        <button class="stage-edit__lore" title={t('Draft a World lore entry from this moment')} onClick={props.onKeepAsLore}>
          {t('📖 Keep as lore')}
        </button>
        {props.onPromote && (
          <button class="stage-edit__promote" title={t('Seed a card from their played lines and put them on stage')} onClick={props.onPromote}>
            {t('🎭 Promote to cast')}
          </button>
        )}
        {/* Rightmost and last, behind a confirm: the one action here that removes
            rather than revises. Disabled mid-turn because the daemon refuses a
            revision while one is running — better to look unavailable than to
            answer a click with an error. */}
        <button class="stage-edit__delete" title={t('Delete this message')} disabled={busy} onClick={props.onDelete}>
          {t('🗑 Delete')}
        </button>
      </div>
      {props.branchNote && <p class="stage-edit__note">{props.branchNote}</p>}
    </div>
  )

  // Message-scoped swipe (Option C): an edited message keeps its alternatives, so
  // its own row carries a `‹n/m›` control — independent of the tail's, which never
  // lands on the same message.
  const msgSwipe =
    mark && mark.variants > 1 && !editing ? (
      <div class="stage-swipe stage-swipe--msg">
        <button disabled={busy || mark.active <= 0} onClick={() => props.onSwipeAt(mark.active - 1)}>◀</button>
        <span>{mark.active + 1}/{mark.variants}</span>
        <button disabled={busy || mark.active >= mark.variants - 1} onClick={() => props.onSwipeAt(mark.active + 1)}>▶</button>
        <button class="stage-swipe__drop" title={t('Remove this version')} disabled={busy} onClick={() => props.onDropTake(mark.active)}>✕</button>
      </div>
    ) : null

  switch (item.kind) {
    case 'assistant': {
      // A directed line (Phase 6) the user authored into the scene, or a routed
      // line the meta-narrator handed to a character (Worlds W3): attribute it
      // to the speaking character (or the narrator) with 🎭, not to the card's
      // main character, and drop the (possibly misleading) card avatar.
      const attributed = item.directed || item.routed
      const speaker = attributed ? (item.actor?.trim() || t('Narrator')) : character?.name
      // Directed and routed are BOTH attributed, but they are not the same thing
      // and used to render identically: a line you wrote into the scene yourself
      // and a line the meta-narrator invented came out as the same unadorned
      // "🎭 Narrator" beat, side by side, with nothing to tell them apart. The wire
      // has always distinguished them; only the UI collapsed them.
      const mark = item.directed ? '✍' : '🎭'
      const kindClass = item.directed ? ' stage-row--directed' : item.routed ? ' stage-row--routed' : ''
      const kindTitle = item.directed ? t('You wrote this line into the scene') : t('The meta-narrator handed this beat to a character')
      return (
        <div class={`stage-row stage-row--assistant${kindClass}${editing ? ' stage-row--editing' : ''}`}>
          {!attributed && character?.avatar ? (
            <img class="stage-avatar" src={character.avatar} alt="" />
          ) : (
            <div class="stage-avatar stage-avatar--blank" aria-hidden="true">{attributed ? mark : initial(character?.name)}</div>
          )}
          <div class="stage-row__body">
            {speaker && (
              <span class="stage-row__name" title={attributed ? kindTitle : undefined}>
                {attributed ? `${mark} ${speaker}` : speaker}
              </span>
            )}
            {editing ? editBox : (
              <>
                <Markdown class="stage-bubble" text={item.text} onClick={onBubbleClick} />
                {item.images && <ImageGallery images={item.images} />}
              </>
            )}
            {item.streaming && <span class="stage-caret" aria-hidden="true">▋</span>}
            {msgSwipe}
            {isLast && !editing && (
              <div class="stage-controls">
                {tail && tail.variants > 1 && (
                  <div class="stage-swipe">
                    <button disabled={busy || tail.active <= 0} onClick={() => props.onSwipe(tail.active - 1)}>◀</button>
                    <span>{tail.active + 1}/{tail.variants}</span>
                    <button disabled={busy || tail.active >= tail.variants - 1} onClick={() => props.onSwipe(tail.active + 1)}>▶</button>
                  </div>
                )}
                <button class="stage-regen" disabled={busy} title={t('Regenerate')} onClick={props.onRetry}>↻</button>
                {/* The guided twin sits beside the plain one rather than replacing
                    it: "just roll again" is the common case and stays one click,
                    while "roll again, but shorter" gets a box to say so in. */}
                <button
                  class="stage-regen stage-regen--guided"
                  disabled={busy}
                  title={t('Regenerate with guidance')}
                  onClick={props.onOpenGuide}
                >↻✎</button>
                <button
                  class="stage-continue"
                  disabled={busy || !props.canContinue}
                  title={props.canContinue ? t('Continue') : t("Continue isn't available for this model — assistant-prefill continue is Anthropic-only")}
                  onClick={() => props.canContinue && props.onContinue()}
                >⤸</button>
              </div>
            )}
            {isLast && !editing && props.guiding && (
              <div class="stage-guide">
                <input
                  class="stage-guide__text"
                  autofocus
                  value={props.guiding.text}
                  placeholder={t('What should be different this time?')}
                  onInput={(e) =>
                    props.onGuideChange({ ...props.guiding!, text: (e.target as HTMLInputElement).value })
                  }
                  onKeyDown={(e) => {
                    if (e.key === 'Enter') props.onSubmitGuide()
                    if (e.key === 'Escape') props.onCancelGuide()
                  }}
                />
                <div class="stage-guide__actions">
                  {/* Phrased as an opt-out because showing the withdrawn take is
                      the default, and it is the default because guidance is
                      usually relative — "shorter" than WHAT. Ticking this is for
                      when the last attempt went somewhere you want no trace of. */}
                  <label class="stage-guide__blind" title={t('Useful when the last attempt went somewhere you want no trace of')}>
                    <input
                      type="checkbox"
                      checked={props.guiding.ignorePrior}
                      onChange={(e) =>
                        props.onGuideChange({ ...props.guiding!, ignorePrior: (e.target as HTMLInputElement).checked })
                      }
                    />
                    {t('Start fresh — hide the previous attempt')}
                  </label>
                  <button class="stage-guide__go" disabled={busy} onClick={props.onSubmitGuide}>{t('Regenerate')}</button>
                  <button class="stage-guide__cancel" onClick={props.onCancelGuide}>{t('Cancel')}</button>
                </div>
              </div>
            )}
          </div>
        </div>
      )
    }
    case 'user':
      // A [Direction] steer (Phase 6b / cast.speak) reads as a de-emphasized OOC
      // note — the user directed the story, they didn't say this as their
      // character — so it never renders as a player bubble.
      if (item.directive) {
        return <div class="stage-row stage-row--direction">🎬 {item.text}</div>
      }
      return (
        <div class={`stage-row stage-row--user${editing ? ' stage-row--editing' : ''}`}>
          <div class="stage-row__body">
            {editing ? editBox : (
              <>
                <Markdown class="stage-bubble" text={item.text} onClick={onBubbleClick} />
                {item.images && <ImageGallery images={item.images} />}
              </>
            )}
            {msgSwipe}
          </div>
        </div>
      )
    case 'tool': {
      // actor_spawn brings a cast member on stage — attribute the row to the
      // actor (from the call's args) rather than showing the raw tool name, so a
      // play scene reads as "who just spoke."
      const actor = item.name === 'actor_spawn' ? (item.args as { actor?: string } | undefined)?.actor : undefined
      if (actor) return <div class="stage-row stage-row--actor">🎭 {actor}</div>
      // Stage keeps tool rows to a single quiet line — but a tool that RETURNED
      // an image (generate_image, a scene backdrop) has produced something the
      // scene is about, so the picture shows even though the call stays folded.
      return (
        <div class="stage-row stage-row--tool">
          <span>· {item.name}</span>
          {item.images && <ImageGallery images={item.images} />}
        </div>
      )
    }
    case 'error':
      return <div class="stage-row stage-row--error">{item.text}</div>
    case 'system':
    case 'notice':
    case 'hatch':
      return <div class="stage-row stage-row--note">{item.text}</div>
    case 'compaction':
      return <div class="stage-divider">{t('— summary —')}</div>
    case 'clear':
      return <div class="stage-divider">{t('— cleared —')}</div>
    default:
      return null
  }
}
