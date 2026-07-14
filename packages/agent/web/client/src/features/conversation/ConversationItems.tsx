import type { Item } from '../../platform/conversation/store'
import type { RevealFn } from './CompactionDivider'
import { sequenceConversationItems } from './itemSequence'
import { MessageContent } from './MessageContent'
import { ToolGroup } from './ToolGroup'
import type { ToolView } from './types'

export function ConversationItems({
  items,
  toolView,
  onReveal,
  revealingID,
}: {
  items: Item[]
  toolView: ToolView
  // Paging in the turns behind a compaction divider. Optional so the component
  // stays renderable from a test (and from any host that has not wired it).
  onReveal?: RevealFn
  revealingID?: string
}) {
  return (
    <>
      {sequenceConversationItems(items, toolView).map((entry) =>
        entry.kind === 'tool-group' ? (
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
