import type { WireEvent } from '../ctrlproto/types'

// The board's pending-worker-approval store (orchestration frontend, phase-B
// follow-on). A swarm worker that parks on a tool call has its approval routed to
// the DISPATCHING SESSION's human card (workerConfirmer), riding that session's
// event stream as an ordinary permission_request whose `agent` names the worker.
// The board already subscribes to that session for phase-B busy, so it sees these
// too — this derives, per worker, whether it is currently blocked on a human, so
// the swarm lane can badge the stalled tile and offer a jump to the session where
// the approval card lives (the card only renders in focus mode, not the board).
//
// Preact-free and pure, per the web layering rules: app.tsx owns the transport
// and folds (sess, event) in here. Keyed by call_id (globally unique —
// "worker-<id>-<seq>"), valued by the worker and its dispatching session. A live
// request/resolved pair is the fast path; a subscribe's snapshot is AUTHORITATIVE
// for its session, which clears an approval that resolved while the board wasn't
// watching (a reconnect that missed the permission_resolved).
export interface PendingApproval {
  agent: string
  sess: string
}
export type BoardApprovals = Record<string, PendingApproval> // call_id -> pending

// applyBoardApproval folds one (sess, event) into the map, returning the SAME
// reference when nothing changed so a caller can skip a re-render cheaply.
export function applyBoardApproval(state: BoardApprovals, sess: string, ev: WireEvent): BoardApprovals {
  switch (ev.type) {
    case 'permission_request': {
      const p = ev.permission
      // A session's OWN tool call carries no agent — only a worker's does.
      if (!p?.agent || !p.call_id) return state
      const cur = state[p.call_id]
      if (cur && cur.agent === p.agent && cur.sess === sess) return state
      return { ...state, [p.call_id]: { agent: p.agent, sess } }
    }
    case 'permission_resolved': {
      const id = ev.resolved?.call_id
      if (!id || !(id in state)) return state
      const next = { ...state }
      delete next[id]
      return next
    }
    case 'snapshot': {
      // Reconcile THIS session's approvals to the snapshot: leave every other
      // session's entries untouched, replace this session's with the worker
      // approvals the snapshot still lists. A no-op unless this session is
      // actually involved (nothing pending now, nothing tracked before).
      const perms = (ev.snapshot?.permissions ?? []).filter((p) => p.agent && p.call_id)
      const hadForSess = Object.values(state).some((a) => a.sess === sess)
      if (perms.length === 0 && !hadForSess) return state
      const next: BoardApprovals = {}
      for (const [callID, a] of Object.entries(state)) {
        if (a.sess !== sess) next[callID] = a
      }
      for (const p of perms) next[p.call_id as string] = { agent: p.agent as string, sess }
      return next
    }
    default:
      return state
  }
}

// waitingByAgent collapses the call→pending map to agent → dispatching session:
// which workers are blocked, and where to answer. A worker has exactly one
// dispatching session, so the mapping is well defined even with several pending
// calls for the same worker.
export function waitingByAgent(state: BoardApprovals): Record<string, string> {
  const out: Record<string, string> = {}
  for (const { agent, sess } of Object.values(state)) out[agent] = sess
  return out
}

// forgetBoardApprovals drops pending approvals for workers no longer present (a
// stopped/removed agent) so a missed resolved can't leak a stale badge. Same
// reference when nothing was dropped.
export function forgetBoardApprovals(state: BoardApprovals, keepAgents: Set<string>): BoardApprovals {
  const drop = Object.keys(state).filter((id) => !keepAgents.has(state[id].agent))
  if (drop.length === 0) return state
  const next = { ...state }
  for (const id of drop) delete next[id]
  return next
}
