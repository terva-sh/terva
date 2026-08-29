// @vitest-environment happy-dom
import { afterEach, describe, expect, it } from 'vitest'
import { cleanup, fireEvent, render, screen } from '@testing-library/preact'
import { applyEvent, itemsFromMessages } from '../../platform/conversation/store'
import type { WireMessage } from '../../platform/ctrlproto/types'
import { ConversationItems } from './ConversationItems'

// Recorded thinking reached this client on the wire and was rendered by nothing:
// blockText() kept only `type === 'text'`, so the summary was parsed, dropped,
// and never seen. These pin the disclosure that closes that, and the boundary
// that keeps it clear of the ephemeral live line.

const live = { epoch: 7, base: 0 }

const withReasoning = (summary: string, text = 'I used the btree.'): WireMessage[] => [
  {
    role: 'assistant',
    content: [
      { type: 'reasoning', summary },
      { type: 'text', text },
    ],
  },
]

const renderItems = (msgs: WireMessage[]) =>
  render(<ConversationItems items={itemsFromMessages(msgs, live)} toolView="full" />)

afterEach(cleanup)

describe('recorded reasoning', () => {
  // Collapsed by default: the complaint was thinking that scrolled past unread,
  // not thinking nobody could find. It must not push the reply off the screen.
  it('renders a collapsed disclosure that does not spill the summary', () => {
    const { container } = renderItems(withReasoning('weighing two indexes'))

    expect(container.querySelector('.reasoning')).toBeTruthy()
    expect(container.querySelector('.reasoning-toggle')?.getAttribute('aria-expanded')).toBe('false')
    expect(container.textContent).not.toContain('weighing two indexes')
    expect(container.textContent).toContain('I used the btree.')
  })

  it('reveals the summary when opened, and keeps the reply', () => {
    const { container } = renderItems(withReasoning('weighing two indexes'))
    fireEvent.click(container.querySelector('.reasoning-toggle')!)

    expect(container.querySelector('.reasoning-summary')?.textContent).toContain('weighing two indexes')
    expect(container.textContent).toContain('I used the btree.')
  })

  // The boundary between the two reasoning paths, and the reason the store tests
  // for TEXT rather than for the block. With "Record thinking" off the summary is
  // blanked and the block left in place, so keying off the block's presence would
  // hang a chevron on essentially every assistant message, each opening onto
  // nothing.
  it('draws no disclosure for a display-only block whose summary was blanked', () => {
    const { container } = renderItems(withReasoning(''))

    expect(container.querySelector('.reasoning')).toBeNull()
    expect(container.textContent).toContain('I used the btree.')
  })

  // 🪤 The one-block case is forgiving: a lone blank summary joins to '', which
  // is falsy, so even an implementation that filtered on the block's PRESENCE
  // would pass it. A turn with several assistant segments records one block per
  // segment, and two blanks join to '\n\n' — truthy, and a chevron opening onto
  // whitespace. This is the case that actually pins the filter on text.
  it('draws no disclosure when a multi-segment turn blanked every summary', () => {
    const msgs: WireMessage[] = [
      {
        role: 'assistant',
        content: [
          { type: 'reasoning', summary: '' },
          { type: 'reasoning', summary: '' },
          { type: 'text', text: 'I used the btree.' },
        ],
      },
    ]
    const { container } = renderItems(msgs)

    expect(container.querySelector('.reasoning')).toBeNull()
    expect(container.textContent).toContain('I used the btree.')
  })

  // A turn that thinks and then calls a tool carries thinking and a tool_call and
  // no text at all — the shape Anthropic emits most. The item-creation guard used
  // to require text or images, so that message made no item and its thinking was
  // dropped on replay.
  it('keeps the thinking of a turn that only called a tool', () => {
    const msgs: WireMessage[] = [
      {
        role: 'assistant',
        content: [
          { type: 'reasoning', summary: 'the btree is narrower' },
          { type: 'tool_call', id: 't1', name: 'read' },
        ],
      },
    ]
    const { container } = renderItems(msgs)
    fireEvent.click(container.querySelector('.reasoning-toggle')!)

    expect(screen.getByText(/the btree is narrower/)).toBeTruthy()
  })
})

describe('recorded reasoning on the live path', () => {
  // Replay is not the only way in. A turn finishing in front of the user must
  // land the same disclosure as the same turn read back after a reload.
  it('attaches the summary when the finalized message arrives', () => {
    const streamed = applyEvent([], { type: 'text_delta', delta: 'I used the btree.' })
    const done = applyEvent(streamed, {
      type: 'assistant_message',
      message: {
        role: 'assistant',
        content: [
          { type: 'text', text: 'I used the btree.' },
          { type: 'reasoning', summary: 'weighing two indexes' },
        ],
      },
    })

    const item = done[done.length - 1]
    expect(item.kind).toBe('assistant')
    expect(item.kind === 'assistant' && item.reasoning).toBe('weighing two indexes')
    expect(item.kind === 'assistant' && item.streaming).toBe(false)
  })

  it('leaves reasoning unset when the turn recorded none', () => {
    const done = applyEvent([], {
      type: 'assistant_message',
      message: { role: 'assistant', content: [{ type: 'text', text: 'done' }] },
    })

    const item = done[done.length - 1]
    expect(item.kind === 'assistant' && item.reasoning).toBeUndefined()
  })
})
