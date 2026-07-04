// The transcript model: a flat, ordered list of render items derived from the
// ctrlproto event stream (and the initial snapshot). Kept separate from the
// wire types so rendering has one stable shape to switch on.
import type { WireEvent, WireMessage, WireBlock } from './ctrlproto'

export type Item =
  | { kind: 'user'; id: string; text: string }
  | { kind: 'assistant'; id: string; text: string; streaming: boolean }
  | { kind: 'tool'; id: string; name: string; args: unknown; result?: string; error?: boolean }
  | { kind: 'error'; id: string; text: string }
  // host-injected (synthetic) user-role message, e.g. a continue-on-open-work
  // nudge — shown as a de-emphasized system note, not a user bubble.
  | { kind: 'system'; id: string; text: string }
  // one-shot host-originated notice (an extension command's display/error/insert
  // result), shown in-stream but never persisted — dropped on the next snapshot.
  | { kind: 'notice'; id: string; level: string; ext?: string; text: string }

let seq = 0
const nextID = () => `i${++seq}`

function blockText(blocks: WireBlock[] | undefined): string {
  return (blocks ?? [])
    .filter((b) => b.type === 'text' && b.text)
    .map((b) => b.text as string)
    .join('')
}

// itemsFromMessages rebuilds the transcript from a snapshot's messages,
// attaching each tool_result to its tool_call by id.
export function itemsFromMessages(msgs: WireMessage[]): Item[] {
  const out: Item[] = []
  const byCall = new Map<string, Extract<Item, { kind: 'tool' }>>()
  for (const m of msgs) {
    const text = blockText(m.content)
    if (text) {
      out.push(
        m.synthetic
          ? { kind: 'system', id: nextID(), text }
          : m.role === 'user'
            ? { kind: 'user', id: nextID(), text }
            : { kind: 'assistant', id: nextID(), text, streaming: false },
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
      if (!text) return items
      if (ev.message?.synthetic) return [...items, { kind: 'system', id: nextID(), text }]
      return [...items, { kind: 'user', id: nextID(), text }]
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
      const last = items[items.length - 1]
      if (last && last.kind === 'assistant' && last.streaming) {
        return [...items.slice(0, -1), { ...last, streaming: false }]
      }
      const text = blockText(ev.message?.content)
      if (text) return [...items, { kind: 'assistant', id: nextID(), text, streaming: false }]
      return items
    }
    case 'tool_call':
      return [...items, { kind: 'tool', id: ev.id ?? nextID(), name: ev.name ?? '', args: ev.args }]
    case 'tool_result': {
      const idx = items.findIndex((it) => it.kind === 'tool' && it.id === ev.id)
      if (idx < 0) return items
      const t = items[idx] as Extract<Item, { kind: 'tool' }>
      const updated = { ...t, result: blockText(ev.content), error: ev.is_error }
      return [...items.slice(0, idx), updated, ...items.slice(idx + 1)]
    }
    case 'error':
      return [...items, { kind: 'error', id: nextID(), text: ev.error ?? 'unknown error' }]
    case 'notice': {
      const n = ev.notice
      if (!n?.text) return items
      return [...items, { kind: 'notice', id: nextID(), level: n.level, ext: n.ext, text: n.text }]
    }
    default:
      return items
  }
}
