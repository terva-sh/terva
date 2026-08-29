import { useMemo, useState } from 'preact/hooks'

import { t } from '../../i18n'
import { renderMarkdown } from '../../markdown'

// ReasoningDisclosure shows the thinking RECORDED on one assistant message,
// collapsed behind a chevron.
//
// It is not the live reasoning line. That one streams from `reasoning_delta`,
// lives in SessionState.reasoning, and is dropped the moment the turn ends —
// which is the complaint this exists to answer: the thinking scrolled past
// before it could be read. This half is transcript, so it survives a reload and
// a scroll away.
//
// 🔑 It appears only when "Record thinking" (reasoning_summary) is on. With it
// off the summary is blanked and the block left behind, so `summary` is empty
// and nothing renders — see reasoningSummary in the store. That is the default,
// and it is why an assistant message usually has no chevron at all.
//
// The control follows CompactionDivider deliberately: the panel has no
// global expand-all to ride (the TUI's ctrl+o has no counterpart here), and a
// per-item disclosure is the pattern this surface already teaches.
export function ReasoningDisclosure({
  summary,
  defaultOpen = false,
}: {
  summary: string
  // True when this is the NEWEST recorded thinking and no turn is currently
  // streaming its own. The newest block earns the space because it explains the
  // reply being read; every older one collapses so a long session does not
  // become a wall of stale deliberation.
  defaultOpen?: boolean
}) {
  // null means "follow defaultOpen". A block the reader has actually clicked
  // keeps their choice; one they have not follows the newest-open rule as later
  // turns supersede it. Seeding useState with defaultOpen instead would freeze
  // the value at mount, and the block would stay open forever.
  const [override, setOverride] = useState<boolean | null>(null)
  const open = override ?? defaultOpen
  // Markdown, parsed once per summary rather than on every token delta of the
  // next turn — the same trap CompactionDivider documents. Cheap while closed,
  // but the body outlives the turn now, so the list re-renders around it.
  const html = useMemo(() => renderMarkdown(summary), [summary])
  if (!summary) return null

  return (
    <div class="reasoning">
      <button
        type="button"
        class="reasoning-toggle"
        aria-expanded={open}
        onClick={() => setOverride(!open)}
        title={t('Show the thinking recorded for this reply')}
      >
        <span class="reasoning-chev">{open ? '▾' : '▸'}</span>
        {t('thinking')}
      </button>
      {open && <div class="reasoning-summary md" dangerouslySetInnerHTML={{ __html: html }} />}
    </div>
  )
}
