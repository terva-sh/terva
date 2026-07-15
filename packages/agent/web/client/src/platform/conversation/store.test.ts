import { describe, it, expect } from 'vitest'
import { applyEvent, itemsFromMessages, isSafeImageMime, type Item } from './store'
import type { WireEvent, WireMessage } from '../ctrlproto/types'

// Every message here belongs to the live transcript, starting at index 0.
const LIVE = { epoch: 1, base: 0 }

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
    const [u] = userItems(itemsFromMessages(msgs, LIVE))
    expect(u.text).toBe('look')
    expect(u.images).toEqual([{ mime: 'image/png', data: 'AAAA' }])
  })

  it('surfaces an image-only user message (no text)', () => {
    const items = itemsFromMessages([{ role: 'user', content: [imgBlock('BBBB')] }], LIVE)
    expect(userItems(items)).toHaveLength(1)
    expect(userItems(items)[0].images).toHaveLength(1)
  })

  it('skips size-only image blocks (carrier without image-data)', () => {
    const items = itemsFromMessages([{ role: 'user', content: [{ type: 'text', text: 'hi' }, imgBlock()] }], LIVE)
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
    const tool = itemsFromMessages(msgs, LIVE).find((i) => i.kind === 'tool') as Extract<Item, { kind: 'tool' }>
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
    const [u] = userItems(itemsFromMessages(msgs, LIVE))
    expect(u.images).toEqual([{ mime: 'image/png', data: 'AAAA' }])
  })

  it('yields no images at all when every block is unsafe', () => {
    const svg = { type: 'image', mime_type: 'image/svg+xml', bytes: 3, data: 'PHN2Zz4=' }
    const msgs: WireMessage[] = [{ role: 'user', content: [{ type: 'text', text: 'x' }, svg] }]
    const [u] = userItems(itemsFromMessages(msgs, LIVE))
    expect(u.images).toBeUndefined()
  })
})

// The image tests above cover the attachment path; these pin the rest of the
// reducer — streaming accumulation, tool-result mapping, and the error/notice/
// synthetic item variants — so a broken fold is caught, not just a broken image.

describe('applyEvent — text streaming', () => {
  it('appends consecutive deltas onto one streaming item (not replace)', () => {
    let items = applyEvent([], { type: 'text_delta', delta: 'draw ' })
    items = applyEvent(items, { type: 'text_delta', delta: 'a cat' })
    expect(items).toHaveLength(1)
    const a = items[0] as Extract<Item, { kind: 'assistant' }>
    expect(a.text).toBe('draw a cat')
    expect(a.streaming).toBe(true)
  })

  it('starts a fresh streaming item when the last item is not a live stream', () => {
    const start = applyEvent([], {
      type: 'user_message',
      message: { role: 'user', content: [{ type: 'text', text: 'hi' }] },
    })
    const items = applyEvent(start, { type: 'text_delta', delta: 'reply' })
    expect(items).toHaveLength(2)
    expect(items[1]).toMatchObject({ kind: 'assistant', text: 'reply', streaming: true })
  })

  it('finalizes the stream, keeping the streamed text over the message text', () => {
    let items = applyEvent([], { type: 'text_delta', delta: 'partial' })
    items = applyEvent(items, {
      type: 'assistant_message',
      message: { role: 'assistant', content: [{ type: 'text', text: 'ignored final' }] },
    })
    const a = items[items.length - 1] as Extract<Item, { kind: 'assistant' }>
    expect(a.streaming).toBe(false)
    expect(a.text).toBe('partial')
  })
})

describe('applyEvent — tool results', () => {
  it('maps result text and a failure flag onto the matching tool item', () => {
    let items = applyEvent([], { type: 'tool_call', id: 'c1', name: 'bash' })
    items = applyEvent(items, { type: 'tool_result', id: 'c1', content: [{ type: 'text', text: 'boom' }], is_error: true })
    const tool = items.find((i) => i.kind === 'tool') as Extract<Item, { kind: 'tool' }>
    expect(tool.result).toBe('boom')
    expect(tool.error).toBe(true)
  })

  it('records a successful result with error=false', () => {
    let items = applyEvent([], { type: 'tool_call', id: 'c2', name: 'read' })
    items = applyEvent(items, { type: 'tool_result', id: 'c2', content: [{ type: 'text', text: 'ok' }], is_error: false })
    const tool = items.find((i) => i.kind === 'tool') as Extract<Item, { kind: 'tool' }>
    expect(tool.result).toBe('ok')
    expect(tool.error).toBe(false)
  })

  it('is a no-op for a tool_result whose id matches no pending call', () => {
    const before = applyEvent([], { type: 'tool_call', id: 'c3', name: 'bash' })
    const after = applyEvent(before, { type: 'tool_result', id: 'nope', content: [{ type: 'text', text: 'x' }] })
    expect(after).toBe(before) // same reference — nothing changed
  })

  it('itemsFromMessages maps tool result text and error, not just images', () => {
    const msgs: WireMessage[] = [
      { role: 'assistant', content: [{ type: 'tool_call', id: 't1', name: 'bash' }] },
      { role: 'tool', content: [{ type: 'tool_result', call_id: 't1', content: [{ type: 'text', text: 'oops' }], is_error: true }] },
    ]
    const tool = itemsFromMessages(msgs, LIVE).find((i) => i.kind === 'tool') as Extract<Item, { kind: 'tool' }>
    expect(tool.result).toBe('oops')
    expect(tool.error).toBe(true)
  })
})

describe('applyEvent — error, notice, and synthetic items', () => {
  it('appends an error item, falling back to a generic message', () => {
    expect(applyEvent([], { type: 'error', error: 'kaboom' })).toEqual([
      { kind: 'error', id: expect.any(String), text: 'kaboom' },
    ])
    const [e] = applyEvent([], { type: 'error' })
    expect(e).toMatchObject({ kind: 'error', text: 'unknown error' })
  })

  it('appends a notice carrying level/ext/kind, and drops an empty notice', () => {
    const [n] = applyEvent([], {
      type: 'notice',
      notice: { level: 'error', ext: 'index', text: 'reindexed', kind: 'prompt_rebuilt' },
    })
    expect(n).toMatchObject({ kind: 'notice', level: 'error', ext: 'index', text: 'reindexed', noticeKind: 'prompt_rebuilt' })
    expect(applyEvent([], { type: 'notice', notice: { level: 'info', text: '' } })).toEqual([])
  })

  it('renders a synthetic user message as a system note, not a user bubble', () => {
    const [s] = applyEvent([], {
      type: 'user_message',
      message: { role: 'user', synthetic: true, content: [{ type: 'text', text: 'continue?' }] },
    })
    expect(s).toMatchObject({ kind: 'system', text: 'continue?' })
    const [fromSnapshot] = itemsFromMessages([{ role: 'user', synthetic: true, content: [{ type: 'text', text: 'nudge' }] }], LIVE)
    expect(fromSnapshot).toMatchObject({ kind: 'system', text: 'nudge' })
  })
})

// The stuck-loop hatch's live events (EvStall / EvEscalation) render as in-stream
// hatch notes, toned and glyphed by kind/disposition — the web twin of the TUI
// inline note, so an operator watching the web sees the harness act in real time.
describe('applyEvent — stuck-loop hatch', () => {
  const hatch = (items: Item[]) => items.filter((i): i is Extract<Item, { kind: 'hatch' }> => i.kind === 'hatch')

  it('renders a stall (nudge) as an accent note naming the tool', () => {
    const h = hatch(applyEvent([], { type: 'stall', stall: { axis: 'spin', tool: 'read' } } as WireEvent))
    expect(h).toHaveLength(1)
    expect(h[0]).toMatchObject({ tone: 'accent', glyph: '⟳' })
    expect(h[0].text).toContain('read')
    expect(h[0].text.toLowerCase()).toContain('loop detected')
  })

  it('coalesces repeated stalls into one counting item, leaving others alone', () => {
    let items = applyEvent([], {
      type: 'user_message',
      message: { role: 'user', content: [{ type: 'text', text: 'hi' }] },
    } as WireEvent)
    for (let n = 0; n < 5; n++) {
      items = applyEvent(items, { type: 'stall', stall: { axis: 'spin', tool: 'read' } } as WireEvent)
    }
    const h = hatch(items)
    expect(h).toHaveLength(1) // five nudges → ONE item, not five
    expect(h[0].count).toBe(5)
    expect(h[0].text).toContain('5')
    expect(items).toHaveLength(2) // the user message + the single coalesced hatch
  })

  it('renders a switched escalation as an ok note with the target', () => {
    const h = hatch(
      applyEvent([], {
        type: 'escalation',
        escalation: { disposition: 'switched', to_model: 'gpt-5.6-sol', to_provider: 'openai-codex' },
      } as WireEvent),
    )
    expect(h).toHaveLength(1)
    expect(h[0]).toMatchObject({ tone: 'ok', glyph: '⇗' })
    expect(h[0].text).toContain('gpt-5.6-sol')
    expect(h[0].text).toContain('openai-codex')
  })

  it('renders a failed escalation as an err note carrying the cause', () => {
    const h = hatch(
      applyEvent([], {
        type: 'escalation',
        escalation: { disposition: 'failed', to_model: 'gpt-5.6-sol', detail: 'no credential' },
      } as WireEvent),
    )
    expect(h[0]).toMatchObject({ tone: 'err' })
    expect(h[0].text).toContain('no credential')
  })

  it('renders a declined escalation as a muted note keeping the current model', () => {
    const h = hatch(
      applyEvent([], { type: 'escalation', escalation: { disposition: 'declined', from_model: 'gemma-4-26b' } } as WireEvent),
    )
    expect(h[0]).toMatchObject({ tone: 'muted' })
    expect(h[0].text).toContain('gemma-4-26b')
  })

  it('ignores a hatch event with no payload', () => {
    expect(applyEvent([], { type: 'stall' } as WireEvent)).toHaveLength(0)
    expect(applyEvent([], { type: 'escalation' } as WireEvent)).toHaveLength(0)
  })
})
