import { useCallback, useEffect, useRef, useState } from 'preact/hooks'
import { t } from '../../i18n'
import type { Item } from '../../platform/conversation/store'
import { copyToClipboard } from '../../ui/browser'
import { ConversationItems } from './ConversationItems'
import { QueuedMessage } from './QueuedMessage'
import type { ToolView } from './types'

export function ConversationTimeline({
  items,
  busy,
  toolView,
  queued,
  onEditQueued,
  onCancelQueued,
}: {
  items: Item[]
  busy: boolean
  toolView: ToolView
  queued: string[]
  onEditQueued: (index: number, text: string) => void
  onCancelQueued: (index: number) => void
}) {
  const ref = useRef<HTMLDivElement>(null)
  const pinnedRef = useRef(true)
  const [showJump, setShowJump] = useState(false)

  const onScroll = () => {
    const element = ref.current
    if (!element) return
    // Pinned = within 80px of the bottom. Scrolling up unpins so the stream
    // stops yanking the view back (TUI behavior).
    const nearBottom = element.scrollHeight - element.scrollTop - element.clientHeight < 80
    pinnedRef.current = nearBottom
    setShowJump(!nearBottom)
  }

  useEffect(() => {
    const element = ref.current
    if (element && pinnedRef.current) element.scrollTop = element.scrollHeight
  }, [items, busy, queued])

  const jump = () => {
    const element = ref.current
    if (!element) return
    element.scrollTop = element.scrollHeight
    pinnedRef.current = true
    setShowJump(false)
  }

  // Delegated copy for code blocks: markdown renders a .code-copy button per
  // block (see markdown.ts), and one listener here copies the adjacent <pre>'s
  // text — no per-block Preact handler inside the dangerouslySetInnerHTML.
  const onCodeCopy = useCallback((event: MouseEvent) => {
    const button = (event.target as HTMLElement)?.closest?.('.code-copy') as HTMLElement | null
    if (!button) return
    const text = button.parentElement?.querySelector('pre')?.textContent ?? ''
    if (!text) return
    void copyToClipboard(text).then((ok) => {
      if (!ok) return
      button.classList.add('copied')
      setTimeout(() => button.classList.remove('copied'), 1200)
    })
  }, [])

  return (
    <div class="log-wrap">
      <div class="log" ref={ref} onScroll={onScroll} onClick={onCodeCopy}>
        <ConversationItems items={items} toolView={toolView} />
        {busy && items[items.length - 1]?.kind !== 'assistant' && <div class="working">{t('working…')}</div>}
        {queued.map((text, index) => (
          <QueuedMessage
            key={'q' + index}
            text={text}
            onEdit={(nextText) => onEditQueued(index, nextText)}
            onCancel={() => onCancelQueued(index)}
          />
        ))}
      </div>
      {showJump && (
        <button class="jump" onClick={jump}>
          ↓ {t('jump to latest')}
        </button>
      )}
    </div>
  )
}
