import type { Item } from '../../platform/conversation/store'
import type { SharedFile } from '../../platform/ctrlproto/types'
import type { ToolItem, ToolView } from './types'

export type ConversationSequenceEntry =
  | { kind: 'item'; key: string; item: Item }
  | { kind: 'tool-group'; key: string; tools: ToolItem[] }
  | { kind: 'gap'; key: string; ms: number }
  | { kind: 'shared'; key: string; file: SharedFile }

// How long a silence has to be before the transcript says so out loud.
//
// A per-message clock stamp answers "when did this land"; it does not answer
// "how long was I away", because reading that off two stamps is arithmetic, and
// arithmetic is not a glance. A marker between the rows is. Ten minutes is the
// line because everything below it is the ordinary rhythm of a turn — the marker
// has to stay rare enough that seeing one MEANS something, or it becomes another
// row to skip past.
export const GAP_MARKER_MS = 10 * 60 * 1000

// itemTime is the instant a row happened, when it has one. Only the two message
// kinds carry a wire time; a tool call sits inside a turn and takes its sense
// from the reply that follows, and the dividers are not moments at all.
function itemTime(item: Item): number | undefined {
  if (item.kind !== 'user' && item.kind !== 'assistant') return undefined
  if (!item.time) return undefined
  const ms = new Date(item.time).getTime()
  return isNaN(ms) ? undefined : ms
}

// sequenceConversationItems preserves transcript order and, in grouped mode,
// replaces each consecutive run of tool calls with one group entry. Rendering
// remains a separate concern so another product can present the same sequence
// with different components.
//
// Files a tool shared are LIFTED out of their row and emitted as entries of
// their own, immediately after it. A tool group renders collapsed by default
// (ToolGroup) and even expanded a tool row is chrome the eye skips — so a
// download left inside one is a download the user never finds, which is the
// whole feature not happening. The item keeps carrying them; only the sequence
// pulls them out, so the transcript model stays a faithful mirror of the wire.
export function sequenceConversationItems(items: Item[], toolView: ToolView): ConversationSequenceEntry[] {
  const entries: ConversationSequenceEntry[] = []
  let tools: ToolItem[] = []
  // Shares from the tools in the group being accumulated, emitted after it so
  // they land below the run rather than interleaved through it.
  let pending: SharedFile[] = []
  const flushShares = () => {
    for (const f of pending) entries.push({ kind: 'shared', key: 'sh-' + f.id, file: f })
    pending = []
  }
  const flushTools = () => {
    if (tools.length === 0) return
    entries.push({ kind: 'tool-group', key: 'tg-' + tools[0].id, tools })
    tools = []
    flushShares()
  }

  // The last timed row seen, so a gap is measured between MESSAGES and not
  // across whatever happened to sit between them. Tool calls have no time of
  // their own, so a long turn full of them reads as one gap before the reply
  // rather than a scatter of small ones.
  let prev: number | undefined

  for (const item of items) {
    if (item.kind === 'tool' && item.shared?.length) pending.push(...item.shared)
    if (toolView === 'grouped' && item.kind === 'tool') {
      tools.push(item)
      continue
    }
    flushTools()
    const at = itemTime(item)
    if (at !== undefined) {
      if (prev !== undefined && at - prev >= GAP_MARKER_MS) {
        entries.push({ kind: 'gap', key: 'gap-' + item.id, ms: at - prev })
      }
      prev = at
    }
    entries.push({ kind: 'item', key: item.id, item })
    // Ungrouped mode has no group to flush behind, so a share follows its own
    // row directly.
    if (item.kind === 'tool') flushShares()
  }
  flushTools()
  flushShares()
  return entries
}
