import { useEffect, useRef, useState } from 'preact/hooks'
import type { Client } from '../../platform/ctrlproto/client'
import type { AskRequest, Decision, PermissionRequest, SessionInfo, TailInfo, VariantMark, WireEvent } from '../../platform/ctrlproto/types'
import { type Item, applyEvent, mergeSnapshot } from '../../platform/conversation/store'
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
export function useConversation(client: Client, sessionId: string, generation = 0) {
  const [items, setItems] = useState<Item[]>([])
  const [busy, setBusy] = useState(false)
  const [info, setInfo] = useState<SessionInfo | null>(null)
  const [tail, setTail] = useState<TailInfo | undefined>(undefined)
  // msgMarks are message-scoped swipe positions (Option C) by effective index —
  // an edited older message with alternatives — so the chat can draw a `‹n/m›`
  // control on that message's row. The tail span stays in `tail` (drawn on the last
  // response), so the two never render on the same message.
  const [msgMarks, setMsgMarks] = useState<Map<number, VariantMark>>(new Map())
  // epoch identifies the current transcript; edit/swipe/retry send it so the
  // daemon refuses a stale index (CodeConflict) rather than editing the wrong
  // message. The ref is what mergeSnapshot reads as the held epoch; the state
  // mirror is what action handlers close over.
  const [epoch, setEpoch] = useState(0)
  const epochRef = useRef(0)
  // A pending tool approval / question. Stage ignored both event types entirely,
  // so a turn that reached for a gated tool parked on the daemon FOREVER with no
  // prompt rendered and no way to answer — indistinguishable, from the user's
  // side, from "I sent a message and nothing happened".
  const [permission, setPermission] = useState<PermissionRequest | null>(null)
  const [ask, setAsk] = useState<AskRequest | null>(null)

  useEffect(() => {
    setItems([])
    epochRef.current = 0
    const handle = (ev: WireEvent) => {
      if (ev.type === 'snapshot') {
        const snap = ev.snapshot
        if (!snap) return
        const win = {
          epoch: snap.epoch ?? 0,
          base: snap.base ?? 0,
          total: snap.total ?? (snap.messages?.length ?? 0),
          messages: snap.messages ?? [],
        }
        setItems((prev) => mergeSnapshot(prev, win, epochRef.current))
        epochRef.current = win.epoch
        setEpoch(win.epoch)
        // A snapshot lands at the end of every turn and carries busy
        // authoritatively, so streaming→idle transitions ride it.
        setBusy(!!snap.busy)
        setInfo(snap.session ?? null)
        setTail(snap.tail)
        const marks = new Map<number, VariantMark>()
        for (const m of snap.variant_marks ?? []) if (!m.span) marks.set(m.index, m)
        setMsgMarks(marks)
        // The snapshot carries anything still pending, so a re-subscribe (or a
        // reload) recovers a prompt whose event was missed — the same self-heal
        // the panel gets. Without this, a dropped permission_request would strand
        // the turn even after the subscription came back.
        setPermission(snap.permissions?.[0] ?? null)
        setAsk(snap.asks?.[0] ?? null)
      } else if (ev.type === 'session_updated') {
        // A live model switch / rename / title settle rides session_updated, not a
        // full snapshot (those only land at a turn's end). Fold it into info so the
        // steering drawer's model picker reflects a switch immediately, the way the
        // panel already does — without it the selector stayed stale until the next
        // turn's snapshot.
        const next = ev.info
        if (next) setInfo((cur) => (cur ? { ...cur, ...next } : next))
      } else if (ev.type === 'permission_request') {
        setPermission(ev.permission ?? null)
      } else if (ev.type === 'permission_resolved') {
        // Clear only if it resolved the one we are showing — another client (the
        // panel, a phone) may have answered it, and a stale id must not clear a
        // newer prompt.
        setPermission((p) => (p && ev.resolved?.call_id === p.call_id ? null : p))
      } else if (ev.type === 'ask_request') {
        setAsk(ev.ask ?? null)
      } else if (ev.type === 'ask_resolved') {
        setAsk((a) => (a && ev.resolved?.ask_id === a.ask_id ? null : a))
      } else if (ev.type === 'turn_start') {
        setBusy(true)
      } else if (ev.type === 'turn_end' || ev.type === 'done') {
        // The SECOND clearing path, and the reason a lost frame is no longer fatal.
        // busy used to clear only on the end-of-turn snapshot, so if that snapshot
        // never arrived (a dead subscription, a dropped frame) busy stayed true
        // forever and submit() silently swallowed every later send — the composer
        // wedged at "thinking…" with no way out but a reload.
        //
        // Tradeoff, deliberately taken: "done" means the turn's OUTPUT is complete,
        // not that the engine released the turn slot (endTurn runs just after), so
        // clearing here opens a sub-millisecond window where ▶/post could return
        // ErrBusy. That is recoverable (clearBusyOnReject surfaces it) and no human
        // can click inside it; a permanently wedged composer is not recoverable.
        // The authoritative snapshot follows immediately and re-settles the truth.
        setBusy(false)
      } else if (ev.type === 'error') {
        // An error ends the turn as surely as a done does. The panel clears busy
        // here too; Stage previously folded the row and left busy stuck.
        setBusy(false)
        setItems((it) => applyEvent(it, ev))
      } else {
        setItems((it) => applyEvent(it, ev))
      }
    }
    const pacer = new StreamPacer((ev) => handle(ev))
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
  const retry = () => {
    setBusy(true)
    return clearBusyOnReject(client.send('turn.retry', { epoch }, sessionId))
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
    setPermission(null)
  }
  const answerAsk = (askID: string, text: string) => {
    client.fire('answer', { ask_id: askID, answer: { answer: text } }, sessionId)
    setAsk(null)
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
