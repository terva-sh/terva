import type { Item } from '../../platform/conversation/store'
import type { RevealFn } from './CompactionDivider'
import { sequenceConversationItems } from './itemSequence'
import { MessageContent } from './MessageContent'
import { SharedFileCard } from './SharedFileCard'
import { TimeGap } from './TimeGap'
import { ToolGroup } from './ToolGroup'
import type { ToolView } from './types'

export function ConversationItems({
  items,
  toolView,
  onReveal,
  revealingID,
  sess = '',
  canDownload = false,
}: {
  items: Item[]
  toolView: ToolView
  // Paging in the turns behind a compaction divider. Optional so the component
  // stays renderable from a test (and from any host that has not wired it).
  onReveal?: RevealFn
  revealingID?: string
  // The session a shared file's download URL is scoped to, and whether this
  // carrier serves one at all. Both default off so the component stays
  // renderable from a test; a share then shows as an inert label.
  sess?: string
  canDownload?: boolean
}) {
  return (
    <>
      {sequenceConversationItems(items, toolView).map((entry) =>
        entry.kind === 'gap' ? (
          <TimeGap key={entry.key} ms={entry.ms} />
        ) : entry.kind === 'shared' ? (
          <SharedFileCard key={entry.key} file={entry.file} sess={sess} canDownload={canDownload} />
        ) : entry.kind === 'tool-group' ? (
          <ToolGroup
            key={entry.key}
            tools={entry.tools}
            renderTool={(tool) => <MessageContent key={tool.id} item={tool} toolView="full" />}
          />
        ) : (
          <MessageContent
            key={entry.key}
            item={entry.item}
            toolView={toolView}
            onReveal={onReveal}
            revealing={revealingID === entry.item.id}
          />
        ),
      )}
    </>
  )
}
