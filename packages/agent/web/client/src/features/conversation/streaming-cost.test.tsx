// @vitest-environment happy-dom
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { cleanup, render } from '@testing-library/preact'

// Count the markdown parses. It is the expensive thing in a render (~53µs a message),
// and the bug this file exists for was doing it for EVERY message on EVERY token delta.
const parses = vi.fn((src: string) => `<p>${src}</p>`)
vi.mock('../../markdown', () => ({ renderMarkdown: (src: string) => parses(src) }))

const { applyEvent, itemsFromMessages } = await import('../../platform/conversation/store')
const { ConversationItems } = await import('./ConversationItems')
type Item = ReturnType<typeof itemsFromMessages>[number]

function transcript(n: number): Item[] {
  return itemsFromMessages(
    Array.from({ length: n }, (_, i) => ({
      role: i % 2 ? 'assistant' : 'user',
      content: [{ type: 'text', text: `turn ${i} with **markdown** in it` }],
    })),
    { epoch: 1, base: 0 },
  )
}

beforeEach(() => parses.mockClear())
afterEach(cleanup)

// The list re-renders on every token delta — thirty-odd times a second. Before this,
// renderMarkdown sat in the component body and nothing was memoized, so every finished
// assistant message in the transcript was re-parsed on every one of those renders,
// though not one of them had changed. With both fixes reverted this test reports 520
// parses where it should see 20: twenty-six times the work, every second, forever.
//
// It pins the PROPERTY — unchanged markdown is not re-parsed — and not a mechanism.
// Two things deliver it (the useMemo in AssistantMessage, and memo() on the row) and
// either alone satisfies this test. That is deliberate: the property is what matters,
// and a test that named one mechanism would go green while the other rotted. memo earns
// its keep separately, on render cost this test cannot see; ui/memo.test.ts pins that.
describe('streaming a token into a long conversation', () => {
  it('does not re-parse the markdown of messages that did not change', () => {
    let items = applyEvent(transcript(40), { type: 'text_delta', delta: 'x' })
    const { rerender } = render(<ConversationItems items={items} toolView="full" />)

    const afterFirstPaint = parses.mock.calls.length
    expect(afterFirstPaint).toBeGreaterThan(0) // the finished assistant messages, once each

    for (let d = 0; d < 25; d++) {
      items = applyEvent(items, { type: 'text_delta', delta: 'token ' })
      rerender(<ConversationItems items={items} toolView="full" />)
    }

    // Not one more parse: the streaming row renders as raw text, and every other row is
    // reference-stable, so memo skips it. Without the fix this is 40 messages × 25
    // deltas of wasted work.
    expect(parses.mock.calls.length).toBe(afterFirstPaint)
  })

  it('does parse a message when its text actually changes', () => {
    const items = transcript(4)
    const { rerender } = render(<ConversationItems items={items} toolView="full" />)
    const before = parses.mock.calls.length

    // Same list, one message edited — the memo must NOT hide a real change.
    const edited = items.map((i) => (i.kind === 'assistant' ? { ...i, text: 'rewritten **body**' } : i))
    rerender(<ConversationItems items={edited} toolView="full" />)

    expect(parses.mock.calls.length).toBeGreaterThan(before)
    expect(parses).toHaveBeenCalledWith('rewritten **body**')
  })
})
