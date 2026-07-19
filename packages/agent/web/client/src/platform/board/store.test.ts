import { describe, expect, it } from 'vitest'
import { applyBoardBusy, forgetBoardBusy } from './store'
import type { WireEvent } from '../ctrlproto/types'

const ev = (type: string): WireEvent => ({ type }) as WireEvent

describe('applyBoardBusy', () => {
  it('flips busy on turn_start and idle on turn_end/done', () => {
    let s = applyBoardBusy({}, 's1', ev('turn_start'))
    expect(s.s1).toBe(true)
    s = applyBoardBusy(s, 's1', ev('turn_end'))
    expect(s.s1).toBe(false)
    s = applyBoardBusy(s, 's1', ev('turn_start'))
    s = applyBoardBusy(s, 's1', ev('done'))
    expect(s.s1).toBe(false)
  })

  it('reads busy from a snapshot (the subscribe reply), ignoring its transcript', () => {
    const s = applyBoardBusy({}, 's1', { type: 'snapshot', snapshot: { busy: true } } as WireEvent)
    expect(s.s1).toBe(true)
  })

  it('keys by session so tiles never cross-contaminate', () => {
    let s = applyBoardBusy({}, 's1', ev('turn_start'))
    s = applyBoardBusy(s, 's2', ev('turn_end'))
    expect(s).toEqual({ s1: true, s2: false })
  })

  it('returns the same reference when nothing changed', () => {
    const s = applyBoardBusy({}, 's1', ev('turn_start'))
    expect(applyBoardBusy(s, 's1', ev('turn_start'))).toBe(s) // already true
    expect(applyBoardBusy(s, 's1', ev('text_delta'))).toBe(s) // irrelevant event
    expect(applyBoardBusy(s, '', ev('turn_start'))).toBe(s) // no session id
  })
})

describe('forgetBoardBusy', () => {
  it('drops sessions no longer subscribed', () => {
    const s = { s1: true, s2: false, s3: true }
    expect(forgetBoardBusy(s, new Set(['s1', 's3']))).toEqual({ s1: true, s3: true })
  })

  it('returns the same reference when nothing was dropped', () => {
    const s = { s1: true }
    expect(forgetBoardBusy(s, new Set(['s1', 's2']))).toBe(s)
  })
})
