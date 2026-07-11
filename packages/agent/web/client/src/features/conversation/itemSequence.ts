import type { Item } from '../../platform/conversation/store'
import type { ToolItem, ToolView } from './types'

export type ConversationSequenceEntry =
  | { kind: 'item'; key: string; item: Item }
  | { kind: 'tool-group'; key: string; tools: ToolItem[] }

// sequenceConversationItems preserves transcript order and, in grouped mode,
// replaces each consecutive run of tool calls with one group entry. Rendering
// remains a separate concern so another product can present the same sequence
// with different components.
export function sequenceConversationItems(items: Item[], toolView: ToolView): ConversationSequenceEntry[] {
  if (toolView !== 'grouped') {
    return items.map((item) => ({ kind: 'item', key: item.id, item }))
  }

  const entries: ConversationSequenceEntry[] = []
  let tools: ToolItem[] = []
  const flushTools = () => {
    if (tools.length === 0) return
    entries.push({ kind: 'tool-group', key: 'tg-' + tools[0].id, tools })
    tools = []
  }

  for (const item of items) {
    if (item.kind === 'tool') {
      tools.push(item)
      continue
    }
    flushTools()
    entries.push({ kind: 'item', key: item.id, item })
  }
  flushTools()
  return entries
}
