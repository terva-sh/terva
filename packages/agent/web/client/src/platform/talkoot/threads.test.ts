import { describe, expect, it } from 'vitest'
import type { TalkootCard, TalkootEnvelope, TalkootLine } from '../ctrlproto/types'
import { threadItems } from './store'

let n = 0
const env = (from: string, to: string[], at: string, root = 'c1'): TalkootLine => {
  const e: TalkootEnvelope = { id: `t${++n}`, talkoot: 'crew', from, to, kind: 'message', body: 'x', chain: { root, hops: 1 }, at }
  return { type: 'envelope', at, envelope: e }
}
const at = (s: number) => `2026-10-10T10:00:${String(s).padStart(2, '0')}Z`
const card = (s?: number): TalkootCard => ({ session: 's', member: 'jev', kind: 'ask', id: `k${s ?? 'x'}`, at: s === undefined ? undefined : at(s) })
const kinds = (items: ReturnType<typeof threadItems>) => items.map((i) => (i.kind === 'thread' ? `thread:${i.lines.length}` : i.kind))

describe('threadItems', () => {
  it('gathers one chain of teammate messages into a thread, even when a third member speaks', () => {
    const items = threadItems([
      env('human:sothr', ['helm'], at(1)),
      env('helm', ['jev'], at(2)),
      env('jev', ['kit'], at(3)),
      env('kit', ['helm'], at(4)),
      env('helm', ['human:sothr'], at(5)),
    ])
    expect(kinds(items)).toEqual(['line', 'thread:3', 'line'])
    const th = items[1]
    expect(th.kind === 'thread' && th.members).toEqual(['helm', 'jev', 'kit'])
  })

  it('keeps a later message of a chain in its thread, after a person spoke in between', () => {
    const items = threadItems([
      env('helm', ['jev'], at(1)),
      env('jev', ['helm'], at(2)),
      env('human:sothr', ['helm'], at(3), 'c2'),
      env('jev', ['helm'], at(4)),
    ])
    expect(kinds(items)).toEqual(['thread:3', 'line'])
  })

  it('keeps two chains apart', () => {
    const items = threadItems([env('helm', ['jev'], at(1), 'a'), env('helm', ['jev'], at(2), 'b'), env('jev', ['helm'], at(3), 'a')])
    expect(kinds(items)).toEqual(['thread:2', 'line'])
  })

  it('shows a lone teammate message as a line, with nothing to fold', () => {
    expect(kinds(threadItems([env('helm', ['jev'], at(1))]))).toEqual(['line'])
  })

  it('leaves out the lines the room keeps for its own bookkeeping', () => {
    const items = threadItems([{ type: 'delivery', at: at(1) }, { type: 'roster', at: at(2), by: 'sothr' }])
    expect(kinds(items)).toEqual(['line'])
  })

  it('puts each card at the time it opened, and a card with no time at the end', () => {
    const items = threadItems([env('human:sothr', ['helm'], at(1)), env('helm', ['human:sothr'], at(5))], [card(3), card(), card(9)])
    expect(kinds(items)).toEqual(['line', 'card', 'line', 'card', 'card'])
    expect(items[1].kind === 'card' && items[1].card.id).toBe('k3')
    expect(items[4].kind === 'card' && items[4].card.id).toBe('kx')
  })
})
