import { describe, expect, it } from 'vitest'
import type { Item } from '../../platform/conversation/store'
import { GAP_MARKER_MS, sequenceConversationItems } from './itemSequence'

const user = (id: string): Item => ({ kind: 'user', id, text: id })
const tool = (id: string, name = id): Item => ({ kind: 'tool', id, name, args: {} })

// Timed rows, for the gap markers. `at` is minutes from an arbitrary origin.
const T0 = Date.parse('2026-08-01T09:00:00Z')
const iso = (mins: number) => new Date(T0 + mins * 60_000).toISOString()
const userAt = (id: string, mins: number): Item => ({ kind: 'user', id, text: id, time: iso(mins) })
const replyAt = (id: string, mins: number): Item => ({
  kind: 'assistant',
  id,
  text: id,
  streaming: false,
  time: iso(mins),
})

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

// The gap markers. Per-message stamps say when each row landed; only a marker
// between rows says how long the silence was without the reader doing the
// subtraction.
describe('sequenceConversationItems gap markers', () => {
  const gaps = (entries: ReturnType<typeof sequenceConversationItems>) => entries.filter((e) => e.kind === 'gap')

  it('marks a silence past the threshold and stays quiet below it', () => {
    const brisk = [userAt('u1', 0), replyAt('a1', 2), userAt('u2', 5)]
    expect(gaps(sequenceConversationItems(brisk, 'full'))).toEqual([])

    const away = [userAt('u1', 0), replyAt('a1', 2), userAt('u2', 200)]
    expect(gaps(sequenceConversationItems(away, 'full'))).toEqual([
      { kind: 'gap', key: 'gap-u2', ms: 198 * 60_000 },
    ])
  })

  // Inclusive, so "ten minutes or more" means what it says.
  it('is inclusive at the threshold', () => {
    const at = [userAt('u1', 0), replyAt('a1', GAP_MARKER_MS / 60_000)]
    expect(gaps(sequenceConversationItems(at, 'full'))).toHaveLength(1)
    const under = [userAt('u1', 0), replyAt('a1', GAP_MARKER_MS / 60_000 - 1)]
    expect(gaps(sequenceConversationItems(under, 'full'))).toEqual([])
  })

  it('puts the marker immediately above the row that ended the silence', () => {
    const items = [userAt('u1', 0), userAt('u2', 60)]
    const entries = sequenceConversationItems(items, 'full')
    expect(entries.map((e) => e.key)).toEqual(['u1', 'gap-u2', 'u2'])
  })

  // A long turn is one silence before the reply, not a scatter of small ones:
  // tool calls carry no time of their own, so they never break the measurement.
  it('measures between messages, across the tools in between', () => {
    const items = [userAt('u1', 0), tool('t1'), tool('t2'), replyAt('a1', 45)]
    const entries = sequenceConversationItems(items, 'full')
    expect(entries.map((e) => e.key)).toEqual(['u1', 't1', 't2', 'gap-a1', 'a1'])
    expect(gaps(entries)).toEqual([{ kind: 'gap', key: 'gap-a1', ms: 45 * 60_000 }])
  })

  // An older daemon sends no time at all. The transcript must read exactly as it
  // did before, not sprout a marker off one timed row and one untimed one.
  it('marks nothing when the rows carry no time', () => {
    const items = [user('u1'), user('u2')]
    expect(gaps(sequenceConversationItems(items, 'full'))).toEqual([])
  })

  it('skips an untimed row rather than measuring across it', () => {
    const items = [userAt('u1', 0), user('u2'), replyAt('a1', 5)]
    // u1 → a1 is 5 minutes, under the threshold: the untimed u2 in between must
    // not reset or extend the measurement.
    expect(gaps(sequenceConversationItems(items, 'full'))).toEqual([])
  })

  it('marks gaps in grouped mode too', () => {
    const items = [userAt('u1', 0), tool('t1'), replyAt('a1', 30)]
    const entries = sequenceConversationItems(items, 'grouped')
    expect(entries.map((e) => e.key)).toEqual(['u1', 'tg-t1', 'gap-a1', 'a1'])
  })

  // A shared file must never end up inside a tool group. ToolGroup renders
  // collapsed by default, so a card left in there is a download the user never
  // sees — which is the whole feature not happening.
  describe('shared files', () => {
    const shared = (id: string, file: string): Item => ({
      kind: 'tool',
      id,
      name: 'share_file',
      args: {},
      shared: [{ id: 'shr_' + id, call_id: id, name: file, kind: 'document' }],
    })

    it('lifts a share out of the collapsed group it was published from', () => {
      const items = [tool('t1'), shared('t2', 'report.pdf')]
      const entries = sequenceConversationItems(items, 'grouped')
      expect(entries.map((e) => e.kind)).toEqual(['tool-group', 'shared'])
      expect(entries.map((e) => e.key)).toEqual(['tg-t1', 'sh-shr_t2'])
    })

    it('puts the card after the group rather than splitting the run', () => {
      const items = [tool('t1'), shared('t2', 'a.png'), tool('t3')]
      const entries = sequenceConversationItems(items, 'grouped')
      expect(entries.map((e) => e.key)).toEqual(['tg-t1', 'sh-shr_t2'])
      expect((entries[0] as { tools: Item[] }).tools).toHaveLength(3)
    })

    it('follows its own row when tools are not grouped', () => {
      const items = [user('u1'), shared('t1', 'clip.mp3'), user('u2')]
      const entries = sequenceConversationItems(items, 'full')
      expect(entries.map((e) => e.key)).toEqual(['u1', 't1', 'sh-shr_t1', 'u2'])
    })

    // Even where the tool ROW is suppressed, the card is not: hiding the
    // machinery is not the same as hiding what it produced for you.
    it.each(['minimal', 'hidden'] as const)('survives %s tool view', (toolView) => {
      const items = [shared('t1', 'export.csv')]
      const entries = sequenceConversationItems(items, toolView)
      expect(entries.map((e) => e.kind)).toContain('shared')
    })

    it('emits one card per file, in order', () => {
      const items: Item[] = [
        {
          kind: 'tool',
          id: 't1',
          name: 'share_file',
          args: {},
          shared: [
            { id: 'shr_a', call_id: 't1', name: 'a.png', kind: 'image' },
            { id: 'shr_b', call_id: 't1', name: 'b.pdf', kind: 'document' },
          ],
        },
      ]
      const entries = sequenceConversationItems(items, 'grouped')
      expect(entries.map((e) => e.key)).toEqual(['tg-t1', 'sh-shr_a', 'sh-shr_b'])
    })

    it('leaves a transcript with no shares exactly as it was', () => {
      const items = [user('u1'), tool('t1')]
      expect(sequenceConversationItems(items, 'grouped').map((e) => e.kind)).toEqual(['item', 'tool-group'])
    })
  })
})
