import { useMemo, useState } from 'preact/hooks'

import { t } from '../../i18n'
import { renderMarkdown } from '../../markdown'
import type { Divider, Item } from '../../platform/conversation/store'
import { humanCount } from '../../ui/formatting'

type CompactionItem = Extract<Item, { kind: 'compaction' }>

// onReveal pages in the turns this checkpoint folded away. Optional: a host that
// has not wired it (or a daemon too old to serve conversation.reveal) still gets
// the divider and the summary, just no scrollback behind it.
export type RevealFn = (item: Divider) => void

// CompactionDivider marks where the conversation was compacted: the turns above it
// are no longer in the model's context, `summary` is what stands in for them, and
// the reveal control pages the originals back into the scrollback.
//
// It exists because a compaction summary arrives with role 'user', so the
// transcript used to render it as a user bubble full of raw "## Context Summary"
// markdown — while the turns it replaced vanished without a mark. A boundary the
// user cannot see is a conversation that appears to have simply lost its history.
export function CompactionDivider({
  item,
  onReveal,
  revealing,
}: {
  item: CompactionItem
  onReveal?: RevealFn
  revealing?: boolean
}) {
  const [open, setOpen] = useState(false)
  // A summary is markdown, and the list re-renders on every token delta. Parse it once
  // per summary, not thirty times a second. (Cheap here — the divider only renders its
  // body when open — but the same trap, and the next person to open it by default
  // should not have to rediscover it.)
  const html = useMemo(() => renderMarkdown(item.summary), [item.summary])

  const label =
    item.tokensBefore && item.tokensBefore > 0
      ? t('compacted · ~%s tokens', humanCount(item.tokensBefore))
      : t('compacted')

  return (
    <div class="compaction">
      <div class="compaction-rule">
        <button
          type="button"
          class="compaction-toggle"
          aria-expanded={open}
          onClick={() => setOpen(!open)}
          title={t('Show the summary the model was left with')}
        >
          <span class="compaction-chev">{open ? '▾' : '▸'}</span>
          {label}
        </button>
        {onReveal && !item.revealed && (
          <button
            type="button"
            class="compaction-reveal"
            disabled={revealing}
            onClick={() => onReveal(item)}
            title={t('Load the turns this compaction summarized away')}
          >
            {revealing ? t('loading…') : t('▴ earlier turns')}
          </button>
        )}
      </div>
      {open && <div class="compaction-summary md" dangerouslySetInnerHTML={{ __html: html }} />}
    </div>
  )
}
