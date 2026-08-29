// @vitest-environment happy-dom
import { afterEach, describe, expect, it } from 'vitest'
import { cleanup, render } from '@testing-library/preact'
import { ConversationTimeline } from './ConversationTimeline'
import type { Item } from '../../platform/conversation/store'

// The live thinking block and the newest-open rule, driven through the timeline
// that owns both decisions. The panel mirrors the TUI here on purpose: the same
// wire text, shown the same way, so a user who watches one and then the other
// is not learning two products.

const props = (
  overrides: Partial<Parameters<typeof ConversationTimeline>[0]> = {},
): Parameters<typeof ConversationTimeline>[0] => ({
  items: [],
  busy: false,
  toolView: 'full',
  queued: [],
  onEditQueued: () => {},
  onCancelQueued: () => {},
  ...overrides,
})

const replied = (id: string, reasoning: string, text: string): Item => ({
  kind: 'assistant',
  id,
  text,
  reasoning,
  streaming: false,
})

afterEach(cleanup)

describe('live thinking', () => {
  // 🪤 The regression this pins. The old row kept ONLY the current section:
  // providers separate sections with a blank line, and each one replaced the
  // last, so everything the model thought before its latest sentence was thrown
  // away unread. A block has room to keep it.
  it('accumulates every section instead of keeping only the last', () => {
    const { container } = render(
      <ConversationTimeline
        {...props({ busy: true, reasoning: '**Reading the config**\n\n**Editing the handler**' })}
      />,
    )

    const block = container.querySelector('.reasoning--live')
    expect(block).toBeTruthy()
    expect(block?.textContent).toContain('Reading the config')
    expect(block?.textContent).toContain('Editing the handler')
  })

  // The height cap replaces the truncation the one-line row used to apply. It
  // lives in CSS, so what is pinned here is that the body actually carries the
  // class that caps it — the block sits above the composer, and uncapped growth
  // pushes the input off screen.
  it('caps the body so long thinking cannot push the composer off screen', () => {
    const { container } = render(
      <ConversationTimeline {...props({ busy: true, reasoning: 'considering the handler' })} />,
    )

    expect(container.querySelector('.reasoning-summary--live')).toBeTruthy()
  })

  it('draws nothing when the model sent no thinking', () => {
    const { container } = render(<ConversationTimeline {...props({ busy: true, reasoning: '   \n\n ' })} />)

    expect(container.querySelector('.reasoning--live')).toBeNull()
  })

  it('draws nothing once the turn is over', () => {
    const { container } = render(
      <ConversationTimeline {...props({ busy: false, reasoning: 'left over from the last turn' })} />,
    )

    expect(container.querySelector('.reasoning--live')).toBeNull()
  })
})

describe('the open thinking block', () => {
  // Exactly one block is open, and it is the newest: thinking earns its space
  // while it explains the reply being read, and becomes ballast right after.
  it('opens the newest recorded block and collapses the older one', () => {
    const { container } = render(
      <ConversationTimeline
        {...props({
          items: [
            replied('a1', 'OLDERTHOUGHT about the config', 'first answer'),
            replied('a2', 'NEWERTHOUGHT about the handler', 'second answer'),
          ],
        })}
      />,
    )

    expect(container.textContent).toContain('NEWERTHOUGHT')
    expect(container.textContent).not.toContain('OLDERTHOUGHT')

    const open = container.querySelectorAll('.reasoning-toggle[aria-expanded="true"]')
    expect(open.length).toBe(1)
  })

  // A turn in flight owns the slot. Otherwise the previous turn's thinking sits
  // expanded ABOVE the live block, putting two of them on screen with the stale
  // one on top.
  it('closes the recorded block while a turn streams its own thinking', () => {
    const items = [replied('a1', 'PREVIOUSTHOUGHT about the config', 'first answer')]

    const idle = render(<ConversationTimeline {...props({ items })} />)
    expect(idle.container.textContent).toContain('PREVIOUSTHOUGHT')
    cleanup()

    const busy = render(
      <ConversationTimeline {...props({ items, busy: true, reasoning: 'now considering the handler' })} />,
    )
    expect(busy.container.textContent).not.toContain('PREVIOUSTHOUGHT')
    expect(busy.container.querySelector('.reasoning--live')?.textContent).toContain(
      'now considering the handler',
    )
  })
})
