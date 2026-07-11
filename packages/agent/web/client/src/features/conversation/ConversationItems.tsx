import type { Item } from '../../platform/conversation/store'
import { sequenceConversationItems } from './itemSequence'
import { MessageContent } from './MessageContent'
import { ToolGroup } from './ToolGroup'
import type { ToolView } from './types'

export function ConversationItems({ items, toolView }: { items: Item[]; toolView: ToolView }) {
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
          <MessageContent key={entry.key} item={entry.item} toolView={toolView} />
        ),
      )}
    </>
  )
}
