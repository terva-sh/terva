import { describe, expect, it } from 'vitest'
import type { Item } from '../../platform/conversation/store'
import { sequenceConversationItems } from './itemSequence'

const user = (id: string): Item => ({ kind: 'user', id, text: id })
const tool = (id: string, name = id): Item => ({ kind: 'tool', id, name, args: {} })

describe('sequenceConversationItems', () => {
  it.each(['full', 'minimal', 'hidden'] as const)('preserves every item in %s mode', (toolView) => {
    const items = [user('u1'), tool('t1'), tool('t2')]
    const entries = sequenceConversationItems(items, toolView)
    expect(entries).toEqual([
      { kind: 'item', key: 'u1', item: items[0] },
      { kind: 'item', key: 't1', item: items[1] },
      { kind: 'item', key: 't2', item: items[2] },
    ])
  })

  it('coalesces only consecutive tool runs and preserves surrounding order', () => {
    const items = [tool('t1'), tool('t2'), user('u1'), tool('t3'), user('u2')]
    const entries = sequenceConversationItems(items, 'grouped')
    expect(entries).toEqual([
      { kind: 'tool-group', key: 'tg-t1', tools: [items[0], items[1]] },
      { kind: 'item', key: 'u1', item: items[2] },
      { kind: 'tool-group', key: 'tg-t3', tools: [items[3]] },
      { kind: 'item', key: 'u2', item: items[4] },
    ])
  })

  it('returns an empty sequence without mutating the source array', () => {
    const empty: Item[] = []
    expect(sequenceConversationItems(empty, 'grouped')).toEqual([])

    const items = [tool('t1'), tool('t2')]
    const before = [...items]
    sequenceConversationItems(items, 'grouped')
    expect(items).toEqual(before)
  })
})
