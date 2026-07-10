// The transcript model: a flat, ordered list of render items derived from the
// ctrlproto event stream (and the initial snapshot). Kept separate from the
// wire types so rendering has one stable shape to switch on.
import type { WireEvent, WireMessage, WireBlock } from './ctrlproto'

// ImageAttachment is one rendered image (data: URL source), carried on the
// message/tool items whose wire blocks included image payloads. Empty when the
// carrier delivered size-only blocks (no "image-data" feature) — then the
// bubble shows a metadata line instead.
export interface ImageAttachment {
  mime: string
  data: string // base64
}

export type Item =
  | { kind: 'user'; id: string; text: string; images?: ImageAttachment[] }
  | { kind: 'assistant'; id: string; text: string; streaming: boolean; images?: ImageAttachment[] }
  | { kind: 'tool'; id: string; name: string; args: unknown; result?: string; error?: boolean; images?: ImageAttachment[] }
  | { kind: 'error'; id: string; text: string }
  // host-injected (synthetic) user-role message, e.g. a continue-on-open-work
  // nudge — shown as a de-emphasized system note, not a user bubble.
  | { kind: 'system'; id: string; text: string }
  // one-shot host-originated notice (an extension command's display/error/insert
  // result), shown in-stream but never persisted — dropped on the next snapshot.
  // noticeKind is the wire Notice.kind (the item's own `kind` is the row
  // discriminator): typed notices like prompt_rebuilt can be filtered/styled;
  // unknown kinds fall back to the plain text.
  | { kind: 'notice'; id: string; level: string; ext?: string; text: string; noticeKind?: string }

let seq = 0
const nextID = () => `i${++seq}`

function blockText(blocks: WireBlock[] | undefined): string {
  return (blocks ?? [])
    .filter((b) => b.type === 'text' && b.text)
    .map((b) => b.text as string)
    .join('')
}

// imageAttachments pulls the renderable image blocks out of a content list —
// only those the carrier delivered with data (image-data negotiated); size-only
// blocks are skipped so the result is empty rather than broken <img> tags.
function imageAttachments(blocks: WireBlock[] | undefined): ImageAttachment[] | undefined {
  const out = (blocks ?? [])
    .filter((b) => b.type === 'image' && b.data && isSafeImageMime(b.mime_type ?? 'image/png'))
    .map((b) => ({ mime: b.mime_type ?? 'image/png', data: b.data as string }))
  return out.length ? out : undefined
}

// safeImageMimes is the allowlist of image types the panel renders or
// uploads: raster formats a browser can only ever draw. Everything else —
// image/svg+xml above all — is dropped: tool, MCP, extension, and provider
// blocks can carry an arbitrary mime_type, and an SVG in a data:/blob:
// context is a script container, not a picture.
const safeImageMimes = new Set(['image/png', 'image/jpeg', 'image/gif', 'image/webp'])

// isSafeImageMime reports whether mime is on the render/upload allowlist,
// tolerating parameters and case ("image/PNG; charset=binary").
export function isSafeImageMime(mime: string | undefined): boolean {
  if (!mime) return false
  const bare = mime.toLowerCase().split(';')[0].trim()
  return safeImageMimes.has(bare)
}

// itemsFromMessages rebuilds the transcript from a snapshot's messages,
// attaching each tool_result to its tool_call by id.
export function itemsFromMessages(msgs: WireMessage[]): Item[] {
  const out: Item[] = []
  const byCall = new Map<string, Extract<Item, { kind: 'tool' }>>()
  for (const m of msgs) {
    const text = blockText(m.content)
    const images = imageAttachments(m.content)
    if (text || images) {
      out.push(
        m.synthetic
          ? { kind: 'system', id: nextID(), text }
          : m.role === 'user'
            ? { kind: 'user', id: nextID(), text, images }
            : { kind: 'assistant', id: nextID(), text, streaming: false, images },
      )
    }
    for (const b of m.content ?? []) {
      if (b.type === 'tool_call' && b.id) {
        const t: Extract<Item, { kind: 'tool' }> = { kind: 'tool', id: b.id, name: b.name ?? '', args: b.args }
        byCall.set(b.id, t)
        out.push(t)
      } else if (b.type === 'tool_result' && b.call_id) {
        const t = byCall.get(b.call_id)
        if (t) {
          t.result = blockText(b.content)
          t.error = b.is_error
          t.images = imageAttachments(b.content)
        }
      }
    }
  }
  return out
}

// applyEvent folds one conversation event into the item list, returning a new
// array (immutable for Preact). Non-transcript events (permission/ask/usage/
// turn_end) are handled by the caller and ignored here.
export function applyEvent(items: Item[], ev: WireEvent): Item[] {
  switch (ev.type) {
    case 'user_message': {
      const text = blockText(ev.message?.content)
      const images = imageAttachments(ev.message?.content)
      if (!text && !images) return items
      if (ev.message?.synthetic) return [...items, { kind: 'system', id: nextID(), text }]
      return [...items, { kind: 'user', id: nextID(), text, images }]
    }
    case 'text_delta': {
      const last = items[items.length - 1]
      if (last && last.kind === 'assistant' && last.streaming) {
        const updated = { ...last, text: last.text + (ev.delta ?? '') }
        return [...items.slice(0, -1), updated]
      }
      return [...items, { kind: 'assistant', id: nextID(), text: ev.delta ?? '', streaming: true }]
    }
    case 'assistant_message': {
      // Images (agent-generated) ride the finalized message, never the deltas.
      const images = imageAttachments(ev.message?.content)
      const last = items[items.length - 1]
      if (last && last.kind === 'assistant' && last.streaming) {
        return [...items.slice(0, -1), { ...last, streaming: false, images: images ?? last.images }]
      }
      const text = blockText(ev.message?.content)
      if (text || images) return [...items, { kind: 'assistant', id: nextID(), text, streaming: false, images }]
      return items
    }
    case 'tool_call':
      return [...items, { kind: 'tool', id: ev.id ?? nextID(), name: ev.name ?? '', args: ev.args }]
    case 'tool_result': {
      const idx = items.findIndex((it) => it.kind === 'tool' && it.id === ev.id)
      if (idx < 0) return items
      const t = items[idx] as Extract<Item, { kind: 'tool' }>
      const updated = { ...t, result: blockText(ev.content), error: ev.is_error, images: imageAttachments(ev.content) }
      return [...items.slice(0, idx), updated, ...items.slice(idx + 1)]
    }
    case 'error':
      return [...items, { kind: 'error', id: nextID(), text: ev.error ?? 'unknown error' }]
    case 'notice': {
      const n = ev.notice
      if (!n?.text) return items
      return [...items, { kind: 'notice', id: nextID(), level: n.level, ext: n.ext, text: n.text, noticeKind: n.kind }]
    }
    default:
      return items
  }
}
