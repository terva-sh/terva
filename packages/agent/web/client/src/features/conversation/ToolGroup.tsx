import type { ComponentChildren } from 'preact'
import { useState } from 'preact/hooks'
import { tn } from '../../i18n'
import { summarizeToolNames } from '../../toolsummary'
import type { ToolItem } from './types'

export type { ToolItem } from './types'

// summarizeTools names the tools in a group with counts: "bash ×3, read, edit".
// The formatting lives in the shared summarizeToolNames (pinned byte-for-byte
// against the Go TUI summarizer by packages/tui/testdata/tool_summary_golden.json)
// so the two frontends can't drift.
export function summarizeTools(tools: ToolItem[]): string {
  return summarizeToolNames(tools.map((tool) => tool.name))
}

// ToolGroup collapses a run of tool calls to a single "N tool calls" line
// (with a summary of which tools + any failures), expandable to caller-rendered
// tool details. Injecting detail rendering keeps this component independent of
// the current product's message-bubble presentation.
export function ToolGroup({
  tools,
  renderTool,
}: {
  tools: ToolItem[]
  renderTool: (tool: ToolItem) => ComponentChildren
}) {
  const [expanded, setExpanded] = useState(false)
  const failed = tools.reduce((count, tool) => count + (tool.error ? 1 : 0), 0)
  return (
    <div class="tool-group">
      <button class={`tool-group-head${expanded ? ' open' : ''}`} onClick={() => setExpanded((open) => !open)}>
        <span class="tg-caret">{expanded ? '▾' : '▸'}</span>
        <span class="tg-count">{tn(tools.length, '%d tool call', '%d tool calls')}</span>
        {!expanded && <span class="tg-summary">{summarizeTools(tools)}</span>}
        {failed > 0 && <span class="tg-failed">· {tn(failed, '%d failed', '%d failed')}</span>}
      </button>
      {expanded && <div class="tool-group-body">{tools.map((tool) => renderTool(tool))}</div>}
    </div>
  )
}
