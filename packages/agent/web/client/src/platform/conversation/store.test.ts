import { describe, it, expect } from 'vitest'
import { applyEvent, itemsFromMessages, isSafeImageMime, mergeSnapshot, type Item, type Window } from './store'
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

// A share reaches the client twice — live on the tool_result event, and on
// replay via the tool-role message that persisted it. Both must land on the
// same item, or a card vanishes the moment you refresh the panel.
describe('shared files', () => {
  const share = (id: string, name: string, call = 't1') => ({ id, call_id: call, name, kind: 'document' })

  const toolItem = (items: Item[]) => items.find((i) => i.kind === 'tool') as Extract<Item, { kind: 'tool' }>

  it('lands a live share on its tool item', () => {
    let items = applyEvent([], { type: 'tool_call', id: 't1', name: 'share_file' })
    items = applyEvent(items, {
      type: 'tool_result',
      id: 't1',
      content: [{ type: 'text', text: 'shared' }],
      shared: [share('shr_a', 'report.pdf')],
    })
    expect(toolItem(items).shared).toEqual([share('shr_a', 'report.pdf')])
  })

  it('leaves a result with no shares undefined rather than empty', () => {
    let items = applyEvent([], { type: 'tool_call', id: 't1', name: 'bash' })
    items = applyEvent(items, { type: 'tool_result', id: 't1', content: [{ type: 'text', text: 'ok' }] })
    expect(toolItem(items).shared).toBeUndefined()
  })

  it('joins a replayed share to its call', () => {
    const msgs: WireMessage[] = [
      { role: 'assistant', content: [{ type: 'tool_call', id: 't1', name: 'share_file' }] },
      {
        role: 'tool',
        content: [{ type: 'tool_result', call_id: 't1', content: [{ type: 'text', text: 'shared' }] }],
        shared: [share('shr_a', 'report.pdf')],
      },
    ]
    expect(toolItem(itemsFromMessages(msgs, LIVE)).shared).toEqual([share('shr_a', 'report.pdf')])
  })

  // One tool-role message can carry several calls' results, which is the whole
  // reason call_id is on the record.
  it('routes each replayed share to its own call', () => {
    const msgs: WireMessage[] = [
      {
        role: 'assistant',
        content: [
          { type: 'tool_call', id: 't1', name: 'share_file' },
          { type: 'tool_call', id: 't2', name: 'share_file' },
        ],
      },
      {
        role: 'tool',
        content: [
          { type: 'tool_result', call_id: 't1', content: [] },
          { type: 'tool_result', call_id: 't2', content: [] },
        ],
        shared: [share('shr_a', 'a.png', 't1'), share('shr_b', 'b.pdf', 't2')],
      },
    ]
    const tools = itemsFromMessages(msgs, LIVE).filter((i) => i.kind === 'tool') as Extract<Item, { kind: 'tool' }>[]
    expect(tools.map((t) => t.shared?.map((f) => f.id))).toEqual([['shr_a'], ['shr_b']])
  })

  // A window that starts below the call has no row for the card to sit beside.
  // Dropping it is right: an orphan would render at the top of the transcript,
  // detached from anything that explains it.
  it('drops a share whose call is not in this window', () => {
    const msgs: WireMessage[] = [
      { role: 'tool', content: [{ type: 'tool_result', call_id: 'gone', content: [] }], shared: [share('shr_a', 'a.pdf', 'gone')] },
    ]
    expect(itemsFromMessages(msgs, LIVE).some((i) => i.kind === 'tool')).toBe(false)
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

  it('folds a rejection into a durable error notice quoting the blocked text', () => {
    const items = applyEvent([], { type: 'user_message_rejected', text: 'contains a secret', rejected: 'deploy key is XYZ' })
    expect(items).toHaveLength(1)
    expect(items[0]).toMatchObject({ kind: 'notice', level: 'error' })
    const text = (items[0] as Extract<Item, { kind: 'notice' }>).text
    expect(text).toContain('contains a secret')
    expect(text).toContain('deploy key is XYZ')
  })

  // The rejection row's own comment calls it "a durable transcript row, not a
  // toast", and names the case it exists for: a QUEUED message rejected while
  // nobody is watching. It was built as a plain notice, and mergeSnapshot's
  // keep-filter drops those BY DESIGN — the comment eleven lines up says so
  // ("Notices have neither and are dropped, which is what they are for"). So the
  // exact case it was introduced to prevent lost its record at the very next
  // turn-end snapshot. Two comments in one file, opposite lifetimes, neither
  // enforced by a type.
  it('keeps a rejection notice across the next turn-end snapshot', () => {
    const rejected = applyEvent([], { type: 'user_message_rejected', text: 'contains a secret', rejected: 'deploy key is XYZ' })
    expect(rejected[0]).toMatchObject({ sticky: true })

    const snap: Window = { epoch: 1, base: 0, total: 1, messages: [{ role: 'user', content: [{ type: 'text', text: 'a later turn' }] }] }
    const merged = mergeSnapshot(rejected, snap, 1)

    const kept = merged.filter((i) => i.kind === 'notice')
    expect(kept, 'the user returns to a transcript with no trace that their prompt was refused').toHaveLength(1)
    expect((kept[0] as Extract<Item, { kind: 'notice' }>).text).toContain('contains a secret')
  })

  // The complement: an ORDINARY notice is still ephemeral. Without this, marking
  // everything sticky would satisfy the test above while turning every transient
  // note into permanent clutter.
  it('still drops an ordinary notice at the next snapshot', () => {
    const items = applyEvent([], { type: 'notice', notice: { level: 'info', text: 'reindexed' } })
    expect(items[0]).not.toMatchObject({ sticky: true })

    const snap: Window = { epoch: 1, base: 0, total: 1, messages: [{ role: 'user', content: [{ type: 'text', text: 'a later turn' }] }] }
    expect(mergeSnapshot(items, snap, 1).filter((i) => i.kind === 'notice')).toHaveLength(0)
  })

  it('clips a long rejected prompt so the notice stays a line, not a document', () => {
    const items = applyEvent([], { type: 'user_message_rejected', text: 'too long', rejected: 'y'.repeat(300) })
    const text = (items[0] as Extract<Item, { kind: 'notice' }>).text
    expect(text).toContain('…')
    expect(text.length).toBeLessThan(200)
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

  // Rungs 1–2 are things terva said; 3–4 are things it did. They coalesce on
  // their own glyphs so a pane never reads "nudged 9×" while calls were in fact
  // being blocked and the turn was ending.
  it('counts refusals apart from nudges', () => {
    let items: Item[] = []
    for (let n = 0; n < 4; n++) {
      items = applyEvent(items, { type: 'stall', stall: { axis: 'spin', tool: 'task_update' } } as WireEvent)
    }
    for (let n = 0; n < 2; n++) {
      items = applyEvent(items, { type: 'stall', stall: { axis: 'spin', tool: 'task_update', rung: 3 } } as WireEvent)
    }
    const h = hatch(items)
    expect(h).toHaveLength(2)
    expect(h[0]).toMatchObject({ glyph: '⟳', count: 4 })
    expect(h[1]).toMatchObject({ glyph: '⊘', tone: 'err', count: 2 })
    expect(h[1].text).toContain('task_update')
    expect(h[1].text).toContain('not dispatched')
  })

  it('renders the give-up as a single terminal note carrying the reason', () => {
    const ev = {
      type: 'stall',
      stall: { axis: 'spin', tool: 'task_update', rung: 4, detail: 'ended the turn: task_update repeated 7×' },
    } as WireEvent
    let items = applyEvent([], ev)
    items = applyEvent(items, ev) // delivered twice must not stack
    const h = hatch(items)
    expect(h).toHaveLength(1)
    expect(h[0]).toMatchObject({ tone: 'err', glyph: '⊗' })
    expect(h[0].text).toContain('ended the turn')
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

// The wire has carried a per-message `time` all along and the render model
// dropped it on the floor, which is why the panel could not say when anything
// arrived. These pin the plumbing: a field that silently stops flowing here
// takes the stamps and the gap markers with it and breaks nothing else.
describe('itemsFromMessages — arrival times', () => {
  const AT = '2026-08-01T09:04:00.123456789Z'

  it('carries the wire time onto user and assistant items', () => {
    const msgs: WireMessage[] = [
      { role: 'user', content: [{ type: 'text', text: 'hi' }], time: AT },
      { role: 'assistant', content: [{ type: 'text', text: 'hey' }], time: AT },
    ]
    const items = itemsFromMessages(msgs, LIVE)
    expect(items.map((i) => (i.kind === 'user' || i.kind === 'assistant' ? i.time : null))).toEqual([AT, AT])
  })

  // A [Direction] steer takes a different branch through userRow, and that
  // branch had its own argument list to forget.
  it('carries it through the directive branch too', () => {
    const items = itemsFromMessages([{ role: 'user', content: [{ type: 'text', text: '[Direction] go' }], time: AT }], LIVE)
    expect(items[0].kind === 'user' && items[0].directive).toBe(true)
    expect(items[0].kind === 'user' && items[0].time).toBe(AT)
  })

  it('leaves the field absent when the daemon sends no time', () => {
    const items = itemsFromMessages([{ role: 'user', content: [{ type: 'text', text: 'hi' }] }], LIVE)
    expect(items[0].kind === 'user' && items[0].time).toBeUndefined()
  })
})

// A message carrying attachments leads with the host's preamble block — the one
// naming their absolute staging paths. The model needs it; a reader does not,
// and on a phone it wraps to nine lines above the two the user actually typed.
// The message's own `preamble` flag is the signal to drop it.
describe('attachment preamble suppression', () => {
  const withMessage = (content: { type: string; text?: string }[], extra: Record<string, unknown> = {}) =>
    itemsFromMessages([{ role: 'user', content, ...extra }] as unknown as WireMessage[], LIVE)
  const withAttachments = (content: { type: string; text?: string }[], attachments?: unknown[]) =>
    withMessage(content, attachments ? { attachments, preamble: true } : {})

  it('drops the preamble block and keeps the user text', () => {
    const items = withAttachments(
      [
        { type: 'text', text: '(the user attached files…\n  /home/u/.terva/attachments/s1/att_1-filters.xml — document\n)\n' },
        { type: 'text', text: 'check these filters' },
      ],
      [{ name: 'filters.xml', kind: 'document', size: 12403 }],
    )
    expect(items).toHaveLength(1)
    expect(items[0]).toMatchObject({ kind: 'user', text: 'check these filters' })
    expect((items[0] as { attachments?: unknown[] }).attachments).toHaveLength(1)
  })

  // Without the flag there is no preamble, so nothing may be dropped — this is
  // every message that has ever existed, and eating its first block would be a
  // spectacular regression.
  it('keeps every block when no preamble is declared', () => {
    const items = withAttachments([{ type: 'text', text: 'just a question' }])
    expect(items[0]).toMatchObject({ kind: 'user', text: 'just a question' })
  })

  // An attachment sent with no words of its own still has to produce a row, or
  // the message vanishes from the transcript entirely.
  it('renders a row for an attachment-only message', () => {
    const items = withAttachments(
      [{ type: 'text', text: '(the user attached files…)' }],
      [{ name: 'filters.xml', kind: 'document', size: 12403 }],
    )
    expect(items).toHaveLength(1)
    expect(items[0]).toMatchObject({ kind: 'user', text: '' })
    expect((items[0] as { attachments?: unknown[] }).attachments).toHaveLength(1)
  })

  // The case that made the flag its own field. Every file the message named had
  // been swept, so there is a preamble and NOTHING to label. Keying suppression
  // off the attachment list left the manifest prose sitting in the user's
  // bubble — the exact text the whole mechanism exists to hide.
  it('drops the preamble when every attachment expired', () => {
    const items = withMessage(
      [
        { type: 'text', text: '(the user attached files…\n  (2 further attachment(s) are no longer on disk…)\n)\n' },
        { type: 'text', text: 'check these filters' },
      ],
      { preamble: true, attachments_missing: 2 },
    )
    expect(items).toHaveLength(1)
    expect(items[0]).toMatchObject({ kind: 'user', text: 'check these filters', attachmentsMissing: 2 })
    expect((items[0] as { attachments?: unknown[] }).attachments).toBeUndefined()
  })

  // …and the row still exists when that is ALL the message was: a file dropped
  // with no words, lapsed before the send. Suppressing the preamble without
  // this leaves an empty message that renders as nothing at all.
  it('renders a row for a message whose only content was expired attachments', () => {
    const items = withMessage([{ type: 'text', text: '(the user attached files…)' }], {
      preamble: true,
      attachments_missing: 1,
    })
    expect(items).toHaveLength(1)
    expect(items[0]).toMatchObject({ kind: 'user', text: '', attachmentsMissing: 1 })
  })
})
