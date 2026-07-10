import { describe, it, expect } from 'vitest'
import { applyEvent, itemsFromMessages, isSafeImageMime, type Item } from './store'
import type { WireEvent, WireMessage } from './ctrlproto'

// The store is the client's pure wire→render transform. These tests pin the
// image scenarios: attachments and agent-generated images surface as renderable
// data on the right items, and size-only blocks (a carrier without the
// image-data feature) are skipped rather than rendered as broken <img>.

const imgBlock = (data?: string) => ({ type: 'image', mime_type: 'image/png', bytes: 3, ...(data ? { data } : {}) })

function userItems(items: Item[]) {
  return items.filter((i): i is Extract<Item, { kind: 'user' }> => i.kind === 'user')
}

describe('itemsFromMessages — image capture', () => {
  it('carries a user attachment (text + image) onto the user item', () => {
    const msgs: WireMessage[] = [
      { role: 'user', content: [{ type: 'text', text: 'look' }, imgBlock('AAAA')] },
    ]
    const [u] = userItems(itemsFromMessages(msgs))
    expect(u.text).toBe('look')
    expect(u.images).toEqual([{ mime: 'image/png', data: 'AAAA' }])
  })

  it('surfaces an image-only user message (no text)', () => {
    const items = itemsFromMessages([{ role: 'user', content: [imgBlock('BBBB')] }])
    expect(userItems(items)).toHaveLength(1)
    expect(userItems(items)[0].images).toHaveLength(1)
  })

  it('skips size-only image blocks (carrier without image-data)', () => {
    const items = itemsFromMessages([{ role: 'user', content: [{ type: 'text', text: 'hi' }, imgBlock()] }])
    expect(userItems(items)[0].images).toBeUndefined()
  })

  it('attaches a tool-result screenshot to its tool item', () => {
    const msgs: WireMessage[] = [
      {
        role: 'assistant',
        content: [{ type: 'tool_call', id: 't1', name: 'screenshot' }],
      },
      {
        role: 'tool',
        content: [{ type: 'tool_result', call_id: 't1', content: [imgBlock('CCCC')] }],
      },
    ]
    const tool = itemsFromMessages(msgs).find((i) => i.kind === 'tool') as Extract<Item, { kind: 'tool' }>
    expect(tool.images).toEqual([{ mime: 'image/png', data: 'CCCC' }])
  })
})

describe('applyEvent — image capture', () => {
  it('captures an image on a user_message event', () => {
    const ev: WireEvent = { type: 'user_message', message: { role: 'user', content: [imgBlock('DDDD')] } }
    const items = applyEvent([], ev)
    expect(items).toHaveLength(1)
    expect((items[0] as Extract<Item, { kind: 'user' }>).images).toHaveLength(1)
  })

  it('captures an agent-generated image on assistant_message (fresh item)', () => {
    const ev: WireEvent = {
      type: 'assistant_message',
      message: { role: 'assistant', content: [{ type: 'text', text: 'here' }, imgBlock('EEEE')] },
    }
    const items = applyEvent([], ev)
    const a = items[items.length - 1] as Extract<Item, { kind: 'assistant' }>
    expect(a.text).toBe('here')
    expect(a.images).toEqual([{ mime: 'image/png', data: 'EEEE' }])
    expect(a.streaming).toBe(false)
  })

  it('attaches images when finalizing a streamed assistant reply', () => {
    let items = applyEvent([], { type: 'text_delta', delta: 'draw…' })
    expect((items[0] as Extract<Item, { kind: 'assistant' }>).streaming).toBe(true)
    items = applyEvent(items, {
      type: 'assistant_message',
      message: { role: 'assistant', content: [imgBlock('FFFF')] },
    })
    const a = items[items.length - 1] as Extract<Item, { kind: 'assistant' }>
    expect(a.streaming).toBe(false)
    expect(a.text).toBe('draw…') // streamed text preserved
    expect(a.images).toEqual([{ mime: 'image/png', data: 'FFFF' }])
  })

  it('attaches a tool_result event image to the matching tool item', () => {
    let items = applyEvent([], { type: 'tool_call', id: 't9', name: 'render' })
    items = applyEvent(items, { type: 'tool_result', id: 't9', content: [imgBlock('GGGG')] })
    const tool = items.find((i) => i.kind === 'tool') as Extract<Item, { kind: 'tool' }>
    expect(tool.images).toEqual([{ mime: 'image/png', data: 'GGGG' }])
  })
})

describe('image MIME allowlist', () => {
  it('accepts only browser-safe raster types', () => {
    for (const ok of ['image/png', 'image/jpeg', 'image/gif', 'image/webp', 'IMAGE/PNG', 'image/png; charset=binary']) {
      expect(isSafeImageMime(ok), ok).toBe(true)
    }
    for (const bad of ['image/svg+xml', 'image/svg+xml; charset=utf-8', 'text/html', 'application/pdf', '', undefined]) {
      expect(isSafeImageMime(bad as string | undefined), String(bad)).toBe(false)
    }
  })

  it('drops non-allowlisted wire blocks before they reach the gallery', () => {
    const svg = { type: 'image', mime_type: 'image/svg+xml', bytes: 3, data: 'PHN2Zz4=' }
    const png = imgBlock('AAAA')
    const msgs: WireMessage[] = [{ role: 'user', content: [{ type: 'text', text: 'mixed' }, svg, png] }]
    const [u] = userItems(itemsFromMessages(msgs))
    expect(u.images).toEqual([{ mime: 'image/png', data: 'AAAA' }])
  })

  it('yields no images at all when every block is unsafe', () => {
    const svg = { type: 'image', mime_type: 'image/svg+xml', bytes: 3, data: 'PHN2Zz4=' }
    const msgs: WireMessage[] = [{ role: 'user', content: [{ type: 'text', text: 'x' }, svg] }]
    const [u] = userItems(itemsFromMessages(msgs))
    expect(u.images).toBeUndefined()
  })
})
