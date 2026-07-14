import { useMemo } from 'preact/hooks'

import { t } from '../../i18n'
import { renderMarkdown } from '../../markdown'
import type { Item } from '../../platform/conversation/store'
import { CopyButton } from '../../ui/CopyButton'
import { compact, truncate } from '../../ui/formatting'
import { ImageGallery } from '../../ui/ImageGallery'
import { memo } from '../../ui/memo'
import { ClearDivider } from './ClearDivider'
import { CompactionDivider, type RevealFn } from './CompactionDivider'
import type { ToolView } from './types'

export type { ToolView } from './types'

// AssistantMessage exists to own a hook, and the hook is the point.
//
// renderMarkdown used to sit in the render body, so it re-parsed on EVERY render of
// the list — and the list re-renders on every token delta, 30+ times a second. That
// meant re-parsing the markdown of every finished assistant message in the transcript,
// none of which had changed, dozens of times a second. Measured at ~53µs a message, a
// long conversation spent a fifth of a core on it. On a phone, considerably more.
//
// The memo is keyed on the text, so it also survives the item OBJECT being rebuilt by
// a turn-end snapshot — and it is scoped to the mounted row, so it is collected when
// the row is, rather than accumulating in a module-level cache that would have to
// guess at a bound.
function AssistantMessage({ item }: { item: Extract<Item, { kind: 'assistant' }> }) {
  const html = useMemo(() => renderMarkdown(item.text), [item.text])
  return (
    <div class="msg-wrap assistant-wrap">
      <div class="msg assistant md">
        <div dangerouslySetInnerHTML={{ __html: html }} />
        {item.images && <ImageGallery images={item.images} />}
      </div>
      {item.text && <CopyButton text={item.text} />}
    </div>
  )
}

// Memoized: the conversation list re-renders on every token delta, and without this
// every row in it re-rendered too — though only the one being streamed had changed.
// Rows are reference-stable during a turn, so shallow equality catches the rest.
export const MessageContent = memo(function MessageContent({
  item,
  toolView,
  onReveal,
  revealing,
}: {
  item: Item
  toolView: ToolView
  onReveal?: RevealFn
  revealing?: boolean
}) {
  switch (item.kind) {
    case 'user':
      return (
        <div class="msg-wrap user-wrap">
          <div class="msg user">
            {item.text}
            {item.images && <ImageGallery images={item.images} />}
          </div>
          {item.text && <CopyButton text={item.text} />}
        </div>
      )
    case 'assistant':
      // Stream as raw text (re-parsing markdown per token is wasteful); render
      // markdown once the message finalizes.
      if (item.streaming) {
        return <div class="msg assistant streaming">{item.text}</div>
      }
      return <AssistantMessage item={item} />
    case 'error':
      return <div class="msg err">{item.text}</div>
    case 'compaction':
      return <CompactionDivider item={item} onReveal={onReveal} revealing={revealing} />
    case 'clear':
      return <ClearDivider item={item} onReveal={onReveal} revealing={revealing} />
    case 'system':
      // Host-injected nudge (e.g. continue-on-open-work) — de-emphasized so it
      // doesn't read as something the user typed.
      return (
        <div class="sys-note">
          <span class="sys-tag">{t('auto')}</span>
          {item.text}
        </div>
      )
    case 'notice':
      // A one-shot result from an extension command (display/error). Attributed
      // to its extension, styled red when it's an error.
      return (
        <div class={`sys-note notice${item.level === 'error' ? ' err' : ''}`}>
          {item.ext && <span class="sys-tag">{item.ext}</span>}
          {item.text}
        </div>
      )
    case 'tool':
      if (toolView === 'hidden') return null
      if (toolView === 'minimal') {
        return (
          <div class="tool-min" aria-hidden="true">
            ▸ {item.name}
            {item.error ? ' · ' + t('failed') : ''}
          </div>
        )
      }
      return (
        <div class="tool">
          <div class="tool-head">
            <span class="tool-name">{item.name}</span>
            {item.args != null && <span class="tool-args">{compact(item.args)}</span>}
          </div>
          {item.result && <pre class={`tool-result${item.error ? ' err' : ''}`}>{truncate(item.result, 2000)}</pre>}
          {item.images && <ImageGallery images={item.images} />}
        </div>
      )
  }
})
