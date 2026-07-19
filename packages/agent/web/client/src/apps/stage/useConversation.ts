import { useEffect, useRef, useState } from 'preact/hooks'
import type { Client } from '../../platform/ctrlproto/client'
import type { SessionInfo, TailInfo, VariantMark, WireEvent } from '../../platform/ctrlproto/types'
import { type Item, applyEvent, mergeSnapshot } from '../../platform/conversation/store'
import { PACE_INTERVAL_MS, StreamPacer } from '../../platform/conversation/pacer'

// useConversation subscribes to one session and folds its wire stream into the
// SHARED conversation store — the same store, pacer, and epoch-aware merge the
// panel uses, so the Stage chat is a renderer over platform/conversation, not a
// second transcript implementation. It restores the client's previous onEvent on
// unmount, so the single shared Client hands events to whichever screen is live.
export function useConversation(client: Client, sessionId: string) {
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
      } else {
        setItems((it) => applyEvent(it, ev))
      }
    }
    const pacer = new StreamPacer((ev) => handle(ev))
    const prevOnEvent = client.onEvent
    client.onEvent = (sess, ev) => {
      if (sess === sessionId) pacer.push(ev)
    }
    client.fire('subscribe', null, sessionId)
    // Drive the pacer: ORDERED events (deltas, snapshots) queue and drain on the
    // tick, so one 16ms timer for the screen's life keeps the transcript moving.
    const timer = window.setInterval(() => pacer.tick(), PACE_INTERVAL_MS)
    return () => {
      window.clearInterval(timer)
      client.fire('unsubscribe', null, sessionId)
      client.onEvent = prevOnEvent
    }
  }, [client, sessionId])

  const send = (text: string) => {
    setBusy(true)
    client.fire('prompt', { text }, sessionId)
  }
  // Transcript-revision verbs (Phase 1). They resolve on the daemon and a fresh
  // snapshot re-renders us; a rejected call (stale epoch, busy) surfaces to the
  // caller. retry regenerates keeping the old take; swipe flips the active take.
  const edit = (index: number, text: string) => client.send('message.edit', { epoch, index, text }, sessionId)
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
  return { items, busy, info, tail, msgMarks, epoch, send, edit, swipe, swipeAt, pruneAt, dropAt, retry, continueTurn, fork, discardDraft }
}
