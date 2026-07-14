// @vitest-environment happy-dom
import { afterEach, describe, expect, it } from 'vitest'
import { cleanup, fireEvent, render, screen } from '@testing-library/preact'
import {
  itemsFromMessages,
  mergeSnapshot,
  prependHistory,
  spliceRevealed,
  type Window,
} from '../../platform/conversation/store'
import type { WireMessage } from '../../platform/ctrlproto/types'
import { ConversationItems } from './ConversationItems'

// What the daemon broadcasts after a compaction: the summary standing in for the
// folded-away turns (role 'user', flagged), then the tail it kept.
const messages: WireMessage[] = [
  {
    role: 'user',
    compaction: true,
    tokens_before: 112000,
    content: [{ type: 'text', text: '## Context Summary (compacted)\n\nwe renamed the widget' }],
  },
  { role: 'user', content: [{ type: 'text', text: 'carry on then' }] },
]

const EPOCH = 42
const live = (base = 0) => ({ epoch: EPOCH, base })
const snapshot = () => itemsFromMessages(messages, live())
const snapWindow: Window = { epoch: EPOCH, base: 0, total: 2, messages }
const dividerOf = (items: ReturnType<typeof itemsFromMessages>) => items.find((i) => i.kind === 'compaction')!
const withRevealed = (older: WireMessage[], prevOrdinal = -1, prevClear?: boolean) => {
  const items = snapshot()
  return spliceRevealed(items, dividerOf(items).id, { messages: older, prevOrdinal, prevClear })
}

afterEach(cleanup)

describe('compaction divider', () => {
  // The bug: a compaction summary's role IS 'user', so reading the role first rendered
  // it as a user bubble full of raw "## Context Summary" markdown, and the boundary
  // itself left no mark at all.
  it('renders a compaction summary as a divider, not a user bubble', () => {
    const { container } = render(<ConversationItems items={snapshot()} toolView="full" />)

    expect(container.querySelector('.compaction')).toBeTruthy()
    expect(screen.getByText('carry on then')).toBeTruthy()
    expect(container.textContent).not.toContain('Context Summary')
    expect(container.querySelectorAll('.msg.user')).toHaveLength(1)
  })

  it('carries the pre-compaction token count', () => {
    render(<ConversationItems items={snapshot()} toolView="full" />)
    expect(screen.getByRole('button', { name: /112k tokens/ })).toBeTruthy()
  })

  it('keeps the summary collapsed until asked', () => {
    const { container } = render(<ConversationItems items={snapshot()} toolView="full" />)
    expect(container.querySelector('.compaction-summary')).toBeNull()

    fireEvent.click(screen.getByRole('button', { name: /compacted/ }))
    expect(container.querySelector('.compaction-summary')?.textContent).toContain('we renamed the widget')
  })

  it('offers to load earlier turns, and asks for the right checkpoint', () => {
    const seen: number[] = []
    render(<ConversationItems items={snapshot()} toolView="full" onReveal={(i) => seen.push(i.ordinal)} />)
    fireEvent.click(screen.getByRole('button', { name: /earlier turns/ }))
    // The live transcript's summary is always the LATEST checkpoint.
    expect(seen).toEqual([-1])
  })
})

// A message's identity is (epoch, index). The daemon's transcriptEpoch bumps only on a
// wholesale replace and never on an append, so an index never moves within an epoch —
// which is what makes a windowed snapshot mergeable.
describe('message identity', () => {
  it('is stable across snapshots, so a row does not remount every turn', () => {
    expect(itemsFromMessages(messages, live()).map((i) => i.id)).toEqual(
      itemsFromMessages(messages, live()).map((i) => i.id),
    )
  })

  it('places each message at its transcript index, including inside a window', () => {
    expect(itemsFromMessages(messages, live(70)).map((i) => i.idx)).toEqual([70, 71])
  })
})

describe('spliceRevealed', () => {
  it('prepends the revealed turns above their divider', () => {
    const out = withRevealed([
      { role: 'user', content: [{ type: 'text', text: 'the first thing' }] },
      { role: 'assistant', content: [{ type: 'text', text: 'the reply' }] },
    ])

    expect(out.map((i) => i.kind)).toEqual(['user', 'assistant', 'compaction', 'user'])
    expect(out.filter((i) => i.kind === 'compaction')[0].revealed).toBe(true)
    // Revealed rows are NOT in the live transcript, so they carry no index — which is
    // exactly what tells mergeSnapshot to keep them.
    expect(out.slice(0, 2).every((i) => i.history && i.idx === undefined)).toBe(true)
  })

  it('turns a nested summary into the next divider, chained to its ordinal', () => {
    const out = withRevealed(
      [
        { role: 'user', compaction: true, tokens_before: 40000, content: [{ type: 'text', text: 'older summary' }] },
        { role: 'user', content: [{ type: 'text', text: 'a kept turn' }] },
      ],
      0,
    )

    const dividers = out.filter((i) => i.kind === 'compaction')
    expect(dividers).toHaveLength(2)
    expect(dividers[0].ordinal).toBe(0)
    expect(dividers[0].revealed).toBeFalsy()
    expect(dividers[1].revealed).toBe(true)
  })

  it('drops the result if the divider vanished under it', () => {
    const items = snapshot()
    expect(spliceRevealed(items, 'gone', { messages: [{ role: 'user', content: [] }], prevOrdinal: -1 })).toBe(items)
  })
})

// What stage 2 bought — and it retires a whole mechanism stage 1 needed.
describe('mergeSnapshot', () => {
  const buried: WireMessage[] = [{ role: 'user', content: [{ type: 'text', text: 'the buried turn' }] }]

  // A snapshot lands at the end of EVERY turn. Rebuilding from it discarded any history
  // the user had paged in — so stage 1 kept a cache keyed on the divider's summary text
  // purely to put it back. Merging means it was never lost.
  it('keeps history paged in behind a compaction', () => {
    const merged = mergeSnapshot(withRevealed(buried), snapWindow, EPOCH)

    expect(merged.map((i) => i.kind)).toEqual(['user', 'compaction', 'user'])
    expect(merged[0]).toMatchObject({ text: 'the buried turn', history: true })
  })

  // And the divider must not come back offering to expand again: clicking it would
  // splice the same history in a second time.
  it('carries forward the display state of a row that is still the same row', () => {
    const merged = mergeSnapshot(withRevealed(buried), snapWindow, EPOCH)
    expect(merged.find((i) => i.kind === 'compaction')?.revealed).toBe(true)
  })

  // A new epoch means the transcript was wholesale replaced — compacted, cleared.
  // Everything held describes a conversation that no longer exists, revealed history
  // included, so a rebuild is the CORRECT answer, not a fallback.
  it('rebuilds on a new epoch, discarding stale history', () => {
    const merged = mergeSnapshot(withRevealed(buried), { ...snapWindow, epoch: 99 }, EPOCH)
    expect(merged.some((i) => i.history)).toBe(false)
    expect(merged.map((i) => i.kind)).toEqual(['compaction', 'user'])
  })

  // Rows a live turn painted carry no index — the daemon has not said where they land.
  // The turn-end snapshot supplies the real ones, and the merge drops the placeholders
  // rather than showing the turn twice.
  it('replaces the unplaced rows a live turn painted', () => {
    const optimistic = [...snapshot(), { kind: 'assistant' as const, id: 'opt', text: 'streamed', streaming: false }]
    const settled: WireMessage[] = [...messages, { role: 'assistant', content: [{ type: 'text', text: 'streamed' }] }]

    const merged = mergeSnapshot(optimistic, { epoch: EPOCH, base: 0, total: 3, messages: settled }, EPOCH)

    expect(merged).toHaveLength(3)
    expect(merged.filter((i) => i.kind === 'assistant')).toHaveLength(1)
    expect(merged[2].idx).toBe(2)
  })

  // The window is the TAIL. Rows above it are ours to keep — the snapshot never carried
  // them and never claimed to.
  it('keeps rows above the window', () => {
    const held = itemsFromMessages(
      [
        { role: 'user', content: [{ type: 'text', text: 'old' }] },
        { role: 'user', content: [{ type: 'text', text: 'newer' }] },
      ],
      live(0),
    )
    const tail: WireMessage[] = [{ role: 'user', content: [{ type: 'text', text: 'newer' }] }]

    const merged = mergeSnapshot(held, { epoch: EPOCH, base: 1, total: 2, messages: tail }, EPOCH)

    expect(merged.map((i) => i.idx)).toEqual([0, 1])
    expect(merged).toHaveLength(2)
  })
})

describe('prependHistory', () => {
  // conversation.history walks the LIVE transcript. Its page belongs below any revealed
  // history — which is not in the live transcript at all — and above the window.
  it('inserts below revealed history and above the window', () => {
    const out = prependHistory(
      withRevealed([{ role: 'user', content: [{ type: 'text', text: 'behind the compaction' }] }]),
      {
        epoch: EPOCH,
        base: 0,
        total: 3,
        messages: [{ role: 'user', content: [{ type: 'text', text: 'earlier live turn' }] }],
      },
    )

    expect(out.map((i) => (i.kind === 'compaction' ? 'compaction' : (i as { text: string }).text))).toEqual([
      'behind the compaction',
      'earlier live turn',
      'compaction',
      'carry on then',
    ])
  })
})

// A /clear is a deliberate act — nearer a session boundary than a compaction. The walk
// stops there and crossing it is a second, explicit choice.
describe('the clear floor', () => {
  const postClear: WireMessage[] = [{ role: 'user', content: [{ type: 'text', text: 'after the clear' }] }]

  it('mints a clear divider when a clear sits behind the checkpoint', () => {
    const out = withRevealed(postClear, 0, true)

    expect(out.map((i) => i.kind)).toEqual(['clear', 'user', 'compaction', 'user'])
    const clear = out.find((i) => i.kind === 'clear')!
    expect(clear.ordinal).toBe(0)
    expect(clear.revealed).toBeFalsy()
  })

  it('does not walk past it on its own — the crossing is a separate click', () => {
    const asked: number[] = []
    render(
      <ConversationItems items={withRevealed(postClear, 0, true)} toolView="full" onReveal={(d) => asked.push(d.ordinal)} />,
    )

    expect(screen.queryByRole('button', { name: /earlier turns/ })).toBeNull()
    fireEvent.click(screen.getByRole('button', { name: /show what was cleared/ }))
    expect(asked).toEqual([0])
  })
})
