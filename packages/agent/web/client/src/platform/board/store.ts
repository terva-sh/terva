import { sessionBusy } from '../conversation/lifecycle'
import type { WireEvent } from '../ctrlproto/types'

// The board's live-busy store (orchestration frontend, phase B). For the tiles
// the board holds a subscription to, this derives whether a turn is in flight
// from the SAME events the focus view uses — turn_start / done /
// snapshot.busy — so a tile flips the instant a turn starts instead of waiting
// on the next slow sessions.list poll. Preact-free and pure, per the web
// layering rules: app.tsx owns the transport and feeds (sess, event) in here.
//
// It carries only the boolean, never a transcript: a board subscription opens
// with a full snapshot like any other, but the board wants the flag and drops
// the rest.
export type BoardBusy = Record<string, boolean>

// applyBoardBusy folds one (sess, event) into the map. It returns the SAME
// reference when nothing changed, so a caller can skip a re-render cheaply.
export function applyBoardBusy(state: BoardBusy, sess: string, ev: WireEvent): BoardBusy {
  if (!['turn_start', 'done', 'error', 'snapshot'].includes(ev.type)) return state
  const next = sessionBusy(state[sess] ?? false, ev)
  if (!sess || state[sess] === next) return state
  return { ...state, [sess]: next }
}

// forgetBoardBusy drops entries for sessions no longer in `keep` (unsubscribed
// tiles, deletes) so the map can't grow without bound. Same reference when
// nothing was dropped.
export function forgetBoardBusy(state: BoardBusy, keep: Set<string>): BoardBusy {
  const drop = Object.keys(state).filter((id) => !keep.has(id))
  if (drop.length === 0) return state
  const next = { ...state }
  for (const id of drop) delete next[id]
  return next
}
