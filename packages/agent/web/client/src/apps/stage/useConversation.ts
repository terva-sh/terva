import { useEffect, useRef, useState } from 'preact/hooks'
import type { ClientLike } from '../../platform/ctrlproto/client'
import type { Decision, SessionInfo, WireEvent } from '../../platform/ctrlproto/types'
import { emptySessionState, reduceSession, type SessionState } from '../../platform/conversation/session'
import { PACE_INTERVAL_MS, StreamPacer } from '../../platform/conversation/pacer'

// useConversation subscribes to one session and folds its wire stream into the
// SHARED conversation store — the same store, pacer, and epoch-aware merge the
// panel uses, so the Stage chat is a renderer over platform/conversation, not a
// second transcript implementation. It restores the client's previous onEvent on
// unmount, so the single shared Client hands events to whichever screen is live.
//
// `generation` counts connections: it MUST change whenever the socket reconnects,
// because server-side subscriptions are per-connection and die with the socket
// (ServeConn builds a fresh, empty subs map). Without it this effect ran once per
// (client, sessionId) — so after any reconnect Stage held a subscription the
// daemon no longer had. The socket was open, so prompts still went out and turns
// still ran and persisted, but no events ever came back: the user's own message
// never rendered, busy never cleared, and only a reload recovered it. The panel
// re-subscribes explicitly for exactly this reason (app.tsx); Stage did not.
export function useConversation(client: ClientLike, sessionId: string, generation = 0) {
  // One state object, folded by the shared reducer. Stage used to hold seven
  // useStates here and fold them with its own switch — a second copy of the
  // panel's, which is how it came to be missing the panel's re-subscribe, its
  // permission/ask handling, and its busy-clear on error. See
  // platform/conversation/session.
  const [state, setState] = useState<SessionState>(emptySessionState)
  const { items, busy, info, tail, msgMarks, permission, ask } = state
  const epoch = state.win.epoch
  // The reducer needs the CURRENT state inside an effect that closes over the
  // first one, so mirror it in a ref and drive from there.
  const stateRef = useRef<SessionState>(emptySessionState)
  const apply = (ev: WireEvent) => {
    const next = reduceSession(stateRef.current, ev)
    if (next === stateRef.current) return
    stateRef.current = next
    setState(next)
  }
  // Some state moves OPTIMISTICALLY, ahead of any wire event: busy on send and on
  // a rejected command, a prompt dismissed the moment it is answered. Those go
  // through the same ref, or the next event would fold onto a stale copy and undo
  // them.
  const patch = (fields: Partial<SessionState>) => {
    stateRef.current = { ...stateRef.current, ...fields }
    setState(stateRef.current)
  }
  const setBusy = (value: boolean) => {
    if (stateRef.current.busy !== value) patch({ busy: value })
  }

  useEffect(() => {
    stateRef.current = emptySessionState
    setState(emptySessionState)
    const pacer = new StreamPacer((ev) => apply(ev))
    const prevOnEvent = client.onEvent
    client.onEvent = (sess, ev) => {
      if (sess === sessionId) pacer.push(ev)
    }
    // Re-fired on every connection generation. `fire` is a silent no-op on a
    // closed socket, which is the other half of the bug: a chat opened DURING the
    // reconnect window dropped its subscribe with no error and no retry. Re-running
    // on generation covers that too — when the socket comes back, this runs again.
    client.fire('subscribe', null, sessionId)
    // Drive the pacer: ORDERED events (deltas, snapshots) queue and drain on the
    // tick, so one 16ms timer for the screen's life keeps the transcript moving.
    const timer = window.setInterval(() => pacer.tick(), PACE_INTERVAL_MS)
    return () => {
      window.clearInterval(timer)
      client.fire('unsubscribe', null, sessionId)
      client.onEvent = prevOnEvent
    }
  }, [client, sessionId, generation])

  const send = (text: string) => {
    setBusy(true)
    client.fire('prompt', { text }, sessionId)
  }
  // Transcript-revision verbs (Phase 1). They resolve on the daemon and a fresh
  // snapshot re-renders us; a rejected call (stale epoch, busy) surfaces to the
  // caller. retry regenerates keeping the old take; swipe flips the active take.
  const edit = (index: number, text: string) => client.send('message.edit', { epoch, index, text }, sessionId)
  // deleteAt removes one message from the transcript, in place — not a truncation
  // and not variant cleanup (pruneAt/dropAt below act on a position's alternative
  // takes, leaving the message itself). The daemon splices it out, appends an
  // `amend`/delete row (the original stays in the file for audit) and bumps the
  // epoch, so any other client's in-flight revision is rejected rather than
  // applied to a shifted index.
  const deleteAt = (index: number) => client.send('message.delete', { epoch, index }, sessionId)
  const swipe = (variant: number) => client.send('turn.swipe', { epoch, variant }, sessionId)
  // swipeAt switches a message-scoped variant (an edited older message): the daemon
  // routes turn.swipe with an index to the per-message swipe rather than the tail.
  const swipeAt = (index: number, variant: number) => client.send('turn.swipe', { epoch, index, variant }, sessionId)
  // Variant cleanup (§9): pruneAt collapses a message's alternatives to its active
  // take; dropAt removes one take. Both shrink the swipe markers.
  const pruneAt = (index: number) => client.send('variants.prune', { epoch, index }, sessionId)
  const dropAt = (index: number, variant: number) => client.send('variants.drop', { epoch, index, variant }, sessionId)
  // busy is set optimistically and normally cleared by the snapshot that lands at
  // the turn's end — but a REJECTED command (stale-epoch conflict, "nothing to
  // retry") returns an error RESP with no snapshot, so nothing would clear it and
  // the composer stays disabled at a stuck "thinking…". Clear it on rejection here,
  // then re-throw so the caller's error surface still fires.
  const clearBusyOnReject = <T,>(p: Promise<T>) =>
    p.catch((e: unknown) => {
      setBusy(false)
      throw e
    })
  // retry regenerates the last response. Called bare it is the plain regenerate it
  // has always been — an independent sample from the same prefix. With guidance it
  // steers that one generation ("shorter", "have her refuse instead"); the daemon
  // shows the model the take being replaced unless ignorePrior asks it not to,
  // because guidance is usually relative and needs a referent. The guidance is
  // request-scoped on the daemon — it never lands in the transcript, so the takes
  // you swipe between stay honest about what produced them.
  const retry = (guidance?: string, ignorePrior?: boolean) => {
    setBusy(true)
    const text = guidance?.trim() ?? ''
    return clearBusyOnReject(
      client.send(
        'turn.retry',
        text ? { epoch, guidance: text, ignore_prior: !!ignorePrior } : { epoch },
        sessionId,
      ),
    )
  }
  // continueTurn (Phase 4d) extends the trailing assistant message in place — a
  // prefill continuation. Only offered when the session's provider supports it
  // (info.supports_continue), which the caller checks before calling.
  const continueTurn = () => {
    setBusy(true)
    return clearBusyOnReject(client.send('turn.continue', { epoch }, sessionId))
  }
  // advance runs the next turn on the transcript as it stands — the "▶" knob. It
  // injects nothing: where send writes your line and post.line writes theirs in your
  // words, this asks the model for the next beat, which is what a scene needs after
  // you have narrated into it. No epoch: unlike retry/edit it revises no existing
  // message, it only appends.
  const advance = () => {
    setBusy(true)
    return clearBusyOnReject(client.send('turn.advance', null, sessionId))
  }
  // cancel interrupts the turn in flight. Fire-and-forget like the panel's Stop:
  // Cancel never errors (it is a no-op on an idle session), and the daemon's
  // `done` + snapshot settle busy. A mid-stream cancel KEEPS whatever streamed as
  // a normal truncated assistant message, so the scene stays usable — which is
  // the point: stop the model, then narrate or direct someone else before it
  // continues. post.line/advance reject while a turn runs, so stopping first is
  // the only way in.
  const cancel = () => client.fire('cancel', null, sessionId)
  // Answer a pending approval / question. Cleared locally on send so the prompt
  // dismisses immediately; the daemon's *_resolved event confirms it (and clears
  // it on the other clients watching the same session).
  const decide = (callID: string, decision: Decision) => {
    client.fire('approve', { call_id: callID, decision }, sessionId)
    patch({ permission: null })
  }
  const answerAsk = (askID: string, text: string) => {
    client.fire('answer', { ask_id: askID, answer: { answer: text } }, sessionId)
    patch({ ask: null })
  }
  // fork branches the session at index (inclusive): a new child session that shares
  // the transcript through that message and diverges from it, leaving this one
  // intact. The "diverge from here" move — the coherence-preserving sibling of an
  // edit-as-variant. Returns the new session so the caller can open it.
  const fork = (index: number) => client.send<{ session: SessionInfo }>('sessions.fork', { from_index: index }, sessionId)
  // discardDraft asks the daemon to reclaim this session IF it is still an
  // unpromoted draft (a character opened for preview but never sent into) — the
  // navigate-away cleanup. It is a guarded no-op on a real session, so it is safe
  // to fire on every back-out; fire-and-forget, the session is being left anyway.
  const discardDraft = () => client.fire('sessions.discard_draft', null, sessionId)
  return { items, busy, info, tail, msgMarks, epoch, permission, ask, send, edit, deleteAt, swipe, swipeAt, pruneAt, dropAt, retry, continueTurn, advance, cancel, decide, answerAsk, fork, discardDraft }
}
