import { useEffect, useLayoutEffect, useRef, useState } from 'preact/hooks'
import type { Client } from '../../platform/ctrlproto/client'
import type { AskRequest, CardView } from '../../platform/ctrlproto/types'
import type { Item } from '../../platform/conversation/store'
import { renderMarkdown } from '../../markdown'
import { useConversation } from './useConversation'
import { useAutoGrow } from './autogrow'
import { Steering } from './Steering'
import { SuggestReply } from './SuggestReply'

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
  if (downstream <= 0) return 'Delete this message? This can’t be undone.'
  const s = downstream === 1 ? 'reply was' : 'replies were'
  return (
    `Delete this message? The ${downstream} ${s} written to it and will stay, ` +
    'so the scene may not read straight afterwards. Branch instead to keep this thread intact.'
  )
}

export function Chat(props: {
  client: Client
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
  const [editing, setEditing] = useState<{ idx: number; text: string } | null>(null)
  const [steerOpen, setSteerOpen] = useState(false)
  const [suggestOpen, setSuggestOpen] = useState(false)
  const [error, setError] = useState('')
  // The composer grows with its content (up to the CSS cap); keyed on `draft` so
  // it also fits text dropped in by ✨ Suggest and shrinks back after a send.
  const composerRef = useAutoGrow(draft)

  // Resolve the character (name + avatar) from the session's card, for the
  // avatar-anchored rows and the header.
  useEffect(() => {
    if (!info?.card) return
    client
      .send<CardView>('cards.get', { id: info.card })
      .then((c) => setCharacter({ name: c.name, avatar: c.avatar_url }))
      .catch(() => {})
  }, [info?.card])

  // Stick to the newest message: a chat lands at the bottom (where the composer
  // is) on load, and follows new turns as they stream — unless the reader has
  // scrolled up into history, in which case we leave them where they are.
  const transcriptRef = useRef<HTMLElement>(null)
  const stick = useRef(true)
  useEffect(() => {
    stick.current = true // a freshly opened session starts pinned to its end
  }, [sessionId])
  useLayoutEffect(() => {
    const el = transcriptRef.current
    if (el && stick.current) el.scrollTop = el.scrollHeight
  }, [items, sessionId])
  const onTranscriptScroll = () => {
    const el = transcriptRef.current
    if (el) stick.current = el.scrollHeight - el.scrollTop - el.clientHeight < 80
  }

  const bg = info?.background ? `/media/backgrounds/${info.background}` : ''
  // A set session title wins over the card name (matching the Library's
  // `title || name`), so a rename or ✨-regenerate from Steering shows here; an
  // untitled chat falls back to the character it stars.
  const title = info?.title || character?.name || 'Chat'
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
  const saveEdit = () => {
    if (!editing) return
    guard(edit(editing.idx, editing.text))
    setEditing(null)
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
          ‹ Library
        </button>
        <span class="stage-chat__title">{title}</span>
        <div class="stage-chat__right">
          <span class="stage-status">{busy ? 'thinking…' : ''}</span>
          {/* Lean header: the way back to the panel, and Steering. Everything
              else lives in the drawer's tabs. */}
          <a class="stage-nav-link stage-nav-link--icon" href="/" title="Open the control panel" aria-label="Open the control panel">⌂</a>
          <button class="stage-steer-btn" title="Steering" onClick={() => setSteerOpen(true)}>☰</button>
        </div>
      </header>

      <main class="stage-transcript" ref={transcriptRef} onScroll={onTranscriptScroll}>
        {error && <p class="stage-error" onClick={() => setError('')}>{error}</p>}
        {items.length === 0 && <p class="stage-empty">Say something to begin.</p>}
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
            branchNote={
              editing != null && editing.idx === it.idx && it.idx != null && msgCount - 1 - it.idx >= BRANCH_HINT
                ? `${msgCount - 1 - it.idx} messages below were written to this — Save keeps them, Branch starts a new thread.`
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
            onContinue={() => guard(continueTurn())}
          />
        ))}
      </main>

      {Object.keys(cast).length > 0 && (
        <div class="stage-cast-strip">
          <span class="stage-cast-strip__label">Who speaks?</span>
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
            Approve tool: <code>{permission.tool}</code>
          </div>
          {permission.preview && <pre class="stage-interact__preview">{permission.preview.slice(0, 1500)}</pre>}
          <div class="stage-interact__actions">
            <button class="stage-interact__go" onClick={() => decide(permission.call_id, { allow: true })}>Allow</button>
            <button onClick={() => decide(permission.call_id, { allow: true, remember_tool: true })}>Allow &amp; remember</button>
            <button class="stage-interact__deny" onClick={() => decide(permission.call_id, { allow: false, reason: 'denied by user' })}>Deny</button>
          </div>
        </div>
      )}

      {ask && <AskPrompt request={ask} onAnswer={answerAsk} />}

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
          <span class="stage-working__label">working…</span>
        </div>
      )}

      <footer class="stage-composer">
        <button
          class="stage-suggest-btn"
          title="Suggest a reply — draft your next message with the model"
          aria-label="Suggest a reply"
          onClick={() => setSuggestOpen(true)}
        >
          ✨
        </button>
        {/* Advance: let the scene move on without you saying anything. The other
            half of ✨ — that one drafts YOUR line, this one asks for theirs. */}
        <button
          class="stage-advance-btn"
          title="Advance — let the scene continue without saying anything"
          aria-label="Advance the scene"
          disabled={busy}
          onClick={() => guard(advance())}
        >
          ▶
        </button>
        <textarea
          ref={composerRef}
          value={draft}
          placeholder="Say something…"
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
          <button class="stage-stop-btn" title="Stop — interrupt this turn so you can steer the scene" onClick={cancel}>
            ■ Stop
          </button>
        ) : (
          <button onClick={submit} disabled={!draft.trim()}>Send</button>
        )}
      </footer>

      {steerOpen && <Steering client={client} sessionId={sessionId} info={info} onClose={() => setSteerOpen(false)} />}

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
          <input value={custom} placeholder="Your answer…" onInput={(e) => setCustom((e.target as HTMLInputElement).value)} />
          <button class="stage-interact__go" type="submit" disabled={!custom.trim()}>
            Answer
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
  onContinue: () => void
}) {
  const { item, character, editing, isLast, tail, mark, busy } = props

  // The edit box grows with its content up to a CSS cap (like the composer),
  // keyed on editText so it fits the whole message the instant editing opens —
  // not a 4-line slit you scroll a long entry through. The ref sits idle (null)
  // on every non-editing row, so the per-row hook is a no-op until this one edits.
  const editRef = useAutoGrow(props.editText)

  const editBox = (
    <div class="stage-edit">
      <textarea ref={editRef} value={props.editText} onInput={(e) => props.onEditText((e.target as HTMLTextAreaElement).value)} />
      <div class="stage-edit__actions">
        <button onClick={props.onSaveEdit}>Save</button>
        <button class="stage-edit__cancel" onClick={props.onCancelEdit}>Cancel</button>
        <button class="stage-edit__branch" title="Start a new thread from here" onClick={props.onBranch}>⑂ Branch here</button>
        {mark && mark.variants > 1 && (
          <button class="stage-edit__prune" title="Discard this message's other versions" onClick={props.onPrune}>Keep only this</button>
        )}
        {/* Rightmost and last, behind a confirm: the one action here that removes
            rather than revises. Disabled mid-turn because the daemon refuses a
            revision while one is running — better to look unavailable than to
            answer a click with an error. */}
        <button class="stage-edit__delete" title="Delete this message" disabled={busy} onClick={props.onDelete}>
          🗑 Delete
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
        <button class="stage-swipe__drop" title="Remove this version" disabled={busy} onClick={() => props.onDropTake(mark.active)}>✕</button>
      </div>
    ) : null

  switch (item.kind) {
    case 'assistant': {
      // A directed line (Phase 6) the user authored into the scene, or a routed
      // line the meta-narrator handed to a character (Worlds W3): attribute it
      // to the speaking character (or the narrator) with 🎭, not to the card's
      // main character, and drop the (possibly misleading) card avatar.
      const attributed = item.directed || item.routed
      const speaker = attributed ? (item.actor?.trim() || 'Narrator') : character?.name
      // Directed and routed are BOTH attributed, but they are not the same thing
      // and used to render identically: a line you wrote into the scene yourself
      // and a line the meta-narrator invented came out as the same unadorned
      // "🎭 Narrator" beat, side by side, with nothing to tell them apart. The wire
      // has always distinguished them; only the UI collapsed them.
      const mark = item.directed ? '✍' : '🎭'
      const kindClass = item.directed ? ' stage-row--directed' : item.routed ? ' stage-row--routed' : ''
      const kindTitle = item.directed ? 'You wrote this line into the scene' : 'The meta-narrator handed this beat to a character'
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
              <div class="stage-bubble" onClick={props.onStartEdit} dangerouslySetInnerHTML={{ __html: renderMarkdown(item.text) }} />
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
                <button class="stage-regen" disabled={busy} title="Regenerate" onClick={props.onRetry}>↻</button>
                <button
                  class="stage-continue"
                  disabled={busy || !props.canContinue}
                  title={props.canContinue ? 'Continue' : "Continue isn't available for this model — assistant-prefill continue is Anthropic-only"}
                  onClick={() => props.canContinue && props.onContinue()}
                >⤸</button>
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
              <div class="stage-bubble" onClick={props.onStartEdit} dangerouslySetInnerHTML={{ __html: renderMarkdown(item.text) }} />
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
      return <div class="stage-row stage-row--tool">· {item.name}</div>
    }
    case 'error':
      return <div class="stage-row stage-row--error">{item.text}</div>
    case 'system':
    case 'notice':
    case 'hatch':
      return <div class="stage-row stage-row--note">{item.text}</div>
    case 'compaction':
      return <div class="stage-divider">— summary —</div>
    case 'clear':
      return <div class="stage-divider">— cleared —</div>
    default:
      return null
  }
}
