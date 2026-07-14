// @vitest-environment happy-dom
import { afterEach, describe, expect, it, vi } from 'vitest'
import { cleanup, render } from '@testing-library/preact'
import { h } from 'preact'
import { memo } from './memo'

afterEach(cleanup)

// memo is thirty lines instead of `preact/compat`, so it owes a test.
//
// What it buys, and what streaming-cost.test.tsx cannot see: that cost test measures
// markdown PARSES, and the useMemo inside AssistantMessage would satisfy it on its own.
// memo removes the render and diff of the row entirely — measured at 1.43ms -> 0.25ms
// per token delta over a 250-item list, which is the difference between the list being
// free to stream into and not.
describe('memo', () => {
  it('skips a re-render when the props are shallow-equal', () => {
    const renders = vi.fn()
    const Row = memo(({ text }: { text: string }) => {
      renders()
      return h('div', null, text)
    })

    const { rerender } = render(h(Row, { text: 'a' }))
    expect(renders).toHaveBeenCalledTimes(1)

    rerender(h(Row, { text: 'a' }))
    rerender(h(Row, { text: 'a' }))
    expect(renders).toHaveBeenCalledTimes(1)
  })

  it('re-renders when a prop actually changes — it must not hide a real update', () => {
    const renders = vi.fn()
    const Row = memo(({ text }: { text: string }) => {
      renders()
      return h('div', null, text)
    })

    const { rerender, container } = render(h(Row, { text: 'a' }))
    rerender(h(Row, { text: 'b' }))

    expect(renders).toHaveBeenCalledTimes(2)
    expect(container.textContent).toBe('b')
  })

  // Shallow, and only shallow. A row's item is replaced wholesale when it changes
  // (applyEvent and mergeSnapshot both build new objects), so reference equality is the
  // right test — but a caller who mutates an object in place would be lied to, and had
  // better know it.
  it('compares props by reference, not by value', () => {
    const renders = vi.fn()
    const Row = memo(({ item }: { item: { text: string } }) => {
      renders()
      return h('div', null, item.text)
    })

    const { rerender } = render(h(Row, { item: { text: 'a' } }))
    rerender(h(Row, { item: { text: 'a' } })) // equal by value, NOT by reference
    expect(renders).toHaveBeenCalledTimes(2)
  })

  it('re-renders when a prop is added or removed', () => {
    const renders = vi.fn()
    const Row = memo((p: { a: string; b?: string }) => {
      renders()
      return h('div', null, p.a + (p.b ?? ''))
    })

    const { rerender } = render(h(Row, { a: 'x' }))
    rerender(h(Row, { a: 'x', b: 'y' }))
    expect(renders).toHaveBeenCalledTimes(2)
  })
})
