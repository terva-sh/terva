import type { Item } from '../../platform/conversation/store'

// ToolView mirrors the TUI's ctrl+t tool-display cycle, plus a web-only
// 'grouped' state that collapses consecutive tool calls into one entry.
export type ToolView = 'full' | 'grouped' | 'minimal' | 'hidden'

export type ToolItem = Extract<Item, { kind: 'tool' }>
