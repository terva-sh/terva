import { describe, expect, it } from 'vitest'
import { applyBoardApproval, forgetBoardApprovals, waitingByAgent, type BoardApprovals } from './approvals'
import type { WireEvent } from '../ctrlproto/types'

const req = (call_id: string, agent?: string): WireEvent =>
  ({ type: 'permission_request', permission: { call_id, tool: 'bash', agent } }) as WireEvent
const resolved = (call_id: string): WireEvent =>
  ({ type: 'permission_resolved', resolved: { call_id } }) as WireEvent
const snap = (...perms: Array<{ call_id: string; agent?: string }>): WireEvent =>
  ({ type: 'snapshot', snapshot: { permissions: perms.map((p) => ({ tool: 'bash', ...p })) } }) as WireEvent

describe('applyBoardApproval', () => {
  it('tracks a worker approval on request and clears it on resolve', () => {
    let s = applyBoardApproval({}, 's1', req('worker-wk7-1', 'wk7'))
    expect(s['worker-wk7-1']).toEqual({ agent: 'wk7', sess: 's1' })
    s = applyBoardApproval(s, 's1', resolved('worker-wk7-1'))
    expect(s).toEqual({})
  })

  it('ignores a session\'s own approval (no agent)', () => {
    const s = applyBoardApproval({}, 's1', req('call_1'))
    expect(s).toEqual({})
  })

  it('keys by call_id so several workers are tracked independently', () => {
    let s = applyBoardApproval({}, 's1', req('worker-wk7-1', 'wk7'))
    s = applyBoardApproval(s, 's1', req('worker-wk9-1', 'wk9'))
    expect(waitingByAgent(s)).toEqual({ wk7: 's1', wk9: 's1' })
  })

  it('returns the same reference when nothing changed', () => {
    const s = applyBoardApproval({}, 's1', req('worker-wk7-1', 'wk7'))
    expect(applyBoardApproval(s, 's1', req('worker-wk7-1', 'wk7'))).toBe(s) // same request
    expect(applyBoardApproval(s, 's1', resolved('nope'))).toBe(s) // unknown resolve
    expect(applyBoardApproval(s, 's1', { type: 'text_delta' } as WireEvent)).toBe(s) // irrelevant
    expect(applyBoardApproval(s, 's1', req('x', undefined))).toBe(s) // no agent
  })

  it('seeds a pre-existing stall from a subscribe snapshot', () => {
    const s = applyBoardApproval({}, 's1', snap({ call_id: 'worker-wk7-1', agent: 'wk7' }, { call_id: 'call_2' }))
    expect(waitingByAgent(s)).toEqual({ wk7: 's1' }) // the non-worker perm (no agent) is ignored
  })

  it('treats a snapshot as authoritative for its session, clearing a missed resolve', () => {
    // wk7 was tracked as pending, but by the time the board reconnects the
    // worker has been answered — the fresh snapshot no longer lists it.
    let s: BoardApprovals = { 'worker-wk7-1': { agent: 'wk7', sess: 's1' } }
    s = applyBoardApproval(s, 's1', snap()) // s1's authoritative pending set is now empty
    expect(s).toEqual({})
  })

  it('leaves other sessions untouched when reconciling one session\'s snapshot', () => {
    let s: BoardApprovals = {
      'worker-wk7-1': { agent: 'wk7', sess: 's1' },
      'worker-wk9-1': { agent: 'wk9', sess: 's2' },
    }
    s = applyBoardApproval(s, 's1', snap()) // clears s1's, keeps s2's
    expect(s).toEqual({ 'worker-wk9-1': { agent: 'wk9', sess: 's2' } })
  })

  it('is a no-op snapshot when the session has nothing pending now or before', () => {
    const s: BoardApprovals = { 'worker-wk9-1': { agent: 'wk9', sess: 's2' } }
    expect(applyBoardApproval(s, 's1', snap({ call_id: 'call_1' }))).toBe(s)
  })
})

describe('waitingByAgent', () => {
  it('collapses to agent → dispatching session', () => {
    const s: BoardApprovals = {
      'worker-wk7-1': { agent: 'wk7', sess: 's1' },
      'worker-wk7-2': { agent: 'wk7', sess: 's1' }, // same worker, two calls
    }
    expect(waitingByAgent(s)).toEqual({ wk7: 's1' })
  })
})

describe('forgetBoardApprovals', () => {
  it('drops approvals for workers no longer present', () => {
    const s: BoardApprovals = {
      'worker-wk7-1': { agent: 'wk7', sess: 's1' },
      'worker-wk9-1': { agent: 'wk9', sess: 's2' },
    }
    expect(forgetBoardApprovals(s, new Set(['wk7']))).toEqual({ 'worker-wk7-1': { agent: 'wk7', sess: 's1' } })
  })

  it('returns the same reference when nothing was dropped', () => {
    const s: BoardApprovals = { 'worker-wk7-1': { agent: 'wk7', sess: 's1' } }
    expect(forgetBoardApprovals(s, new Set(['wk7', 'wk9']))).toBe(s)
  })
})
