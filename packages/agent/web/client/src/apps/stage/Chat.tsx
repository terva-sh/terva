import { useEffect, useState } from 'preact/hooks'
import type { Client } from '../../platform/ctrlproto/client'
import type { CardView } from '../../platform/ctrlproto/types'
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

export function Chat(props: { client: Client; sessionId: string; onBack: () => void; onOpenSession: (session: string) => void }) {
  const { client, sessionId, onOpenSession } = props
  const { items, busy, info, tail, msgMarks, send, edit, swipe, swipeAt, pruneAt, dropAt, retry, continueTurn, fork, discardDraft } = useConversation(client, sessionId)
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

  const bg = info?.background ? `/media/backgrounds/${info.background}` : ''
  const title = character?.name || info?.title || 'Chat'
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
          <button class="stage-steer-btn" title="Steering" onClick={() => setSteerOpen(true)}>☰</button>
        </div>
      </header>

      <main class="stage-transcript">
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

      <footer class="stage-composer">
        <button
          class="stage-suggest-btn"
          title="Suggest a reply — draft your next message with the model"
          aria-label="Suggest a reply"
          onClick={() => setSuggestOpen(true)}
        >
          ✨
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
        <button onClick={submit} disabled={busy || !draft.trim()}>Send</button>
      </footer>

      {steerOpen && <Steering client={client} sessionId={sessionId} info={info} onClose={() => setSteerOpen(false)} />}

      {suggestOpen && (
        <SuggestReply
          client={client}
          sessionId={sessionId}
          initialNote={draft}
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

  const editBox = (
    <div class="stage-edit">
      <textarea value={props.editText} onInput={(e) => props.onEditText((e.target as HTMLTextAreaElement).value)} />
      <div class="stage-edit__actions">
        <button onClick={props.onSaveEdit}>Save</button>
        <button class="stage-edit__cancel" onClick={props.onCancelEdit}>Cancel</button>
        <button class="stage-edit__branch" title="Start a new thread from here" onClick={props.onBranch}>⑂ Branch here</button>
        {mark && mark.variants > 1 && (
          <button class="stage-edit__prune" title="Discard this message's other versions" onClick={props.onPrune}>Keep only this</button>
        )}
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
    case 'assistant':
      return (
        <div class="stage-row stage-row--assistant">
          {character?.avatar ? (
            <img class="stage-avatar" src={character.avatar} alt="" />
          ) : (
            <div class="stage-avatar stage-avatar--blank" aria-hidden="true">{initial(character?.name)}</div>
          )}
          <div class="stage-row__body">
            {character?.name && <span class="stage-row__name">{character.name}</span>}
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
                {props.canContinue && (
                  <button class="stage-continue" disabled={busy} title="Continue" onClick={props.onContinue}>⤸</button>
                )}
              </div>
            )}
          </div>
        </div>
      )
    case 'user':
      return (
        <div class="stage-row stage-row--user">
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
