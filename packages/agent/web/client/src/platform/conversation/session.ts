import type {
  AskRequest,
  PermissionRequest,
  SessionInfo,
  TailInfo,
  VariantMark,
  WireEvent,
} from '../ctrlproto/types'
import { type Item, type Window, applyEvent, mergeSnapshot } from './store'

// The non-transcript half of folding a session's event stream.
//
// store.ts already owns the transcript itself (applyEvent, mergeSnapshot). What
// sat OUTSIDE it — busy, session info, a pending permission or ask, the held
// window — was written twice, once in the panel and once in Stage, and it had
// drifted three separate times. Each drift is recorded in Stage's own comments,
// and each reads the same way: the panel had a fold Stage did not.
//
//   - Stage never re-subscribed on reconnect, so after a dropped socket it held a
//     subscription the daemon no longer had: prompts went out, turns ran, and no
//     event ever came back.
//   - Stage ignored permission_request and ask_request entirely, so a turn that
//     reached for a gated tool parked forever with nothing on screen.
//   - Stage folded an `error` row but left busy true, wedging the composer at
//     "thinking…" with no way out but a reload.
//
// None of those were hard problems. They were the same problem: two copies, one
// of which had learned something. reduceSession is one copy.
//
// It is pure and Preact-free — no hooks, no setState, no client — so it can be
// tested directly and hosted by either app's state model.

export interface SessionState {
  /** The rendered transcript. */
  items: Item[]
  /**
   * The live window this client holds: which epoch, and which slice of it.
   * A merge KEEPS what sat above the incoming snapshot's base, so the held base
   * is the LOWER of the two — not the snapshot's. Getting that wrong silently
   * drops paged-in history the moment any turn ends.
   */
  win: { epoch: number; base: number; total: number }
  /** A turn is in flight. */
  busy: boolean
  info: SessionInfo | null
  /** Tail variant span (the swipe control on the last response). */
  tail?: TailInfo
  /** Message-scoped variant marks, by effective index. */
  msgMarks: Map<number, VariantMark>
  permission: PermissionRequest | null
  ask: AskRequest | null
}

export const emptySessionState: SessionState = {
  items: [],
  win: { epoch: 0, base: 0, total: 0 },
  busy: false,
  info: null,
  tail: undefined,
  msgMarks: new Map(),
  permission: null,
  ask: null,
}

/**
 * reduceSession folds one wire event into the shared session state.
 *
 * Events it does not recognise fall through to applyEvent, so a transcript-only
 * event still lands. Events an app handles ITSELF (the panel's queued/skills/
 * cost/surfaces, its session list, the locale) are not this reducer's business:
 * both apps run their own switch alongside this call for those. The split is
 * "what every consumer of a session must do" versus "what this app does with it".
 *
 * Returns the SAME object when nothing changed, so a caller can skip a render.
 */
export function reduceSession(state: SessionState, ev: WireEvent): SessionState {
  switch (ev.type) {
    case 'snapshot': {
      const snap = ev.snapshot
      if (!snap) return state
      const incoming: Window = {
        epoch: snap.epoch ?? 0,
        base: snap.base ?? 0,
        total: snap.total ?? (snap.messages?.length ?? 0),
        messages: snap.messages ?? [],
      }
      const held = state.win
      // Same epoch → we may be holding history below the snapshot's base; keep it.
      // Different epoch → the transcript was rewritten, so the snapshot's base wins.
      const base = held.epoch === incoming.epoch ? Math.min(held.base, incoming.base) : incoming.base
      const marks = new Map<number, VariantMark>()
      for (const m of snap.variant_marks ?? []) if (!m.span) marks.set(m.index, m)
      return {
        ...state,
        items: mergeSnapshot(state.items, incoming, held.epoch),
        win: { epoch: incoming.epoch, base, total: incoming.total },
        // A snapshot lands at the end of every turn and carries busy
        // authoritatively, so streaming→idle transitions ride it.
        busy: !!snap.busy,
        info: snap.session ?? null,
        tail: snap.tail,
        msgMarks: marks,
        // The snapshot carries whatever is still pending, so a re-subscribe or a
        // reload recovers a prompt whose event was missed. Without this a dropped
        // permission_request strands the turn even after the subscription returns.
        permission: snap.permissions?.[0] ?? null,
        ask: snap.asks?.[0] ?? null,
      }
    }

    case 'session_updated': {
      // A live model switch, a rename, a settled title — these ride
      // session_updated, not a full snapshot (those only land at a turn's end).
      // Merging here is what makes a model switch show immediately instead of
      // staying stale until the next turn.
      const next = ev.info
      if (!next) return state
      return { ...state, info: state.info ? { ...state.info, ...next } : next }
    }

    case 'permission_request':
      return { ...state, permission: ev.permission ?? null }

    case 'permission_resolved':
      // Clear only if it resolved the one being shown: another client (the panel,
      // a phone) may have answered it, and a stale id must not clear a NEWER
      // prompt that arrived in between.
      return state.permission && ev.resolved?.call_id === state.permission.call_id
        ? { ...state, permission: null }
        : state

    case 'ask_request':
      return { ...state, ask: ev.ask ?? null }

    case 'ask_resolved':
      return state.ask && ev.resolved?.ask_id === state.ask.ask_id ? { ...state, ask: null } : state

    case 'turn_start':
      return state.busy ? state : { ...state, busy: true }

    case 'turn_end':
    case 'done':
      // The second clearing path, and the reason a lost frame is no longer fatal.
      // busy used to clear only on the end-of-turn snapshot, so a snapshot that
      // never arrived (a dead subscription, a dropped frame) left busy true
      // forever and every later send was silently swallowed.
      //
      // Deliberate tradeoff: "done" means the turn's OUTPUT is complete, not that
      // the engine released the turn slot, so clearing here opens a sub-millisecond
      // window where a follow-up could return ErrBusy. That is recoverable and no
      // human can click inside it; a permanently wedged composer is not.
      return state.busy ? { ...state, busy: false } : state

    case 'error':
      // An error ends the turn as surely as a done does. Folding the row while
      // leaving busy set is exactly the wedge described above.
      return { ...state, busy: false, items: applyEvent(state.items, ev) }

    default: {
      const items = applyEvent(state.items, ev)
      return items === state.items ? state : { ...state, items }
    }
  }
}
