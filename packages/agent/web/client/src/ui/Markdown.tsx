import { useMemo } from 'preact/hooks'

import { renderMarkdown } from '../markdown'

// Markdown owns the memo, and the memo is the point.
//
// renderMarkdown in a render body re-parses on EVERY render of the list — and
// the list re-renders on every token delta, 30+ times a second. Measured at
// ~53µs a message, a long conversation spent a fifth of a core re-parsing
// markdown that had not changed. On a phone, considerably more.
//
// The panel learned this and fixed it inside its own AssistantMessage; Stage,
// the phone-first surface, then re-introduced it by calling renderMarkdown
// inline. Keeping the memo in one shared component is what stops the next
// surface from paying for the lesson a third time.
//
// Keyed on the text, so it survives the item OBJECT being rebuilt by a turn-end
// snapshot, and scoped to the mounted row, so it is collected when the row is
// rather than accumulating in a module-level cache with no bound.
export function Markdown({
  text,
  class: cls,
  onClick,
}: {
  text: string
  class?: string
  onClick?: (event: MouseEvent) => void
}) {
  const html = useMemo(() => renderMarkdown(text), [text])
  return <div class={cls} onClick={onClick} dangerouslySetInnerHTML={{ __html: html }} />
}
