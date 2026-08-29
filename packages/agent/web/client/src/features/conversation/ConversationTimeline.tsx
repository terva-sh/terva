import { useCallback, useMemo } from 'preact/hooks'
import { t, tn } from '../../i18n'
import type { Item } from '../../platform/conversation/store'
import { handleCodeCopyClick } from '../../ui/codecopy'
import { usePinnedTail } from '../../ui/pinnedtail'
import type { RevealFn } from './CompactionDivider'
import { ConversationItems } from './ConversationItems'
import { LiveThinking } from './LiveThinking'
import { QueuedMessage } from './QueuedMessage'
import type { ToolView } from './types'

export function ConversationTimeline({
  items,
  busy,
  reasoning,
  toolView,
  queued,
  onEditQueued,
  onCancelQueued,
  onReveal,
  revealingID,
  earlier,
  onLoadEarlier,
  loadingEarlier,
  sess,
  canDownload,
}: {
  items: Item[]
  busy: boolean
  // The model's live thinking summary, raw off the wire. Shown only while busy
  // and never added to items — it is deliberately not transcript.
  reasoning?: string
  toolView: ToolView
  queued: string[]
  onEditQueued: (index: number, text: string) => void
  onCancelQueued: (index: number) => void
  onReveal?: RevealFn
  revealingID?: string
  // How many messages of the LIVE transcript sit above the window we hold. The
  // snapshot carries only the tail; this is the rest, fetched on demand.
  earlier?: number
  onLoadEarlier?: () => void
  loadingEarlier?: boolean
  // Passed straight through to a shared-file card: the session its download URL
  // is scoped to, and whether this carrier serves one.
  sess?: string
  canDownload?: boolean
}) {
  // Land at the newest message and follow the stream, unless the reader has
  // scrolled up to read something (ui/pinnedtail).
  const { ref, onScroll, showJump, jumpToLatest: jump } = usePinnedTail<HTMLDivElement>([items, busy, queued])
  // Exactly ONE thinking block is open, and a turn in flight owns that slot:
  // its thinking renders live below, and leaving the previous turn's block open
  // as well would put two expanded blocks on screen with the stale one on top.
  // Otherwise the newest recorded thinking is the open one, and every older
  // block collapses back behind its chevron.
  const liveThinking = busy && !!(reasoning ?? '').trim()
  const openThinkingId = useMemo(() => {
    if (liveThinking) return ''
    for (let i = items.length - 1; i >= 0; i--) {
      const item = items[i]
      if (item.kind === 'assistant' && item.reasoning) return item.id
    }
    return ''
  }, [items, liveThinking])

  // Delegated copy for code blocks: markdown renders a .code-copy button per
  // block (see markdown.ts), and one listener here copies the adjacent <pre>'s
  // text — no per-block Preact handler inside the dangerouslySetInnerHTML. The
  // handler itself lives in ui/ so every surface that renders markdown can wire
  // it; it used to be private here, which is why Stage's buttons did nothing.
  const onCodeCopy = useCallback((event: MouseEvent) => {
    handleCodeCopyClick(event)
  }, [])

  return (
    <div class="log-wrap">
      <div class="log" ref={ref} onScroll={onScroll} onClick={onCodeCopy}>
        {!!earlier && earlier > 0 && onLoadEarlier && (
          <div class="load-earlier">
            <button type="button" onClick={onLoadEarlier} disabled={loadingEarlier}>
              {loadingEarlier ? t('loading…') : tn(earlier, '▴ %d earlier message', '▴ %d earlier messages')}
            </button>
          </div>
        )}
        <ConversationItems
          items={items}
          toolView={toolView}
          onReveal={onReveal}
          revealingID={revealingID}
          openThinkingId={openThinkingId}
          sess={sess}
          canDownload={canDownload}
        />
        {busy && items[items.length - 1]?.kind !== 'assistant' && <div class="working">{t('working…')}</div>}
        {busy && <LiveThinking text={reasoning ?? ''} />}
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
