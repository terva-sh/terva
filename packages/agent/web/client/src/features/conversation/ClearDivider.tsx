import { t } from '../../i18n'
import type { Item } from '../../platform/conversation/store'
import type { RevealFn } from './CompactionDivider'

type ClearItem = Extract<Item, { kind: 'clear' }>

// ClearDivider marks a /clear: the point where the user said "done with that, start
// fresh".
//
// It is deliberately NOT the compaction divider with different words. A compaction
// condenses a conversation you are still having, so scrolling up through one is just
// scrolling. A clear is a decision — nearer a session boundary — and the backward
// walk stops here on purpose. Crossing it is a second, separate act, which is why
// the control says what it will do rather than politely offering "earlier turns".
//
// It is an intent boundary, not redaction: those turns are still in the session file
// in plaintext, and --replay raw, export, and session_inspect all read them. Nothing
// here makes a secret pasted before a clear go away, and the UI must not imply it.
export function ClearDivider({
  item,
  onReveal,
  revealing,
}: {
  item: ClearItem
  onReveal?: RevealFn
  revealing?: boolean
}) {
  return (
    <div class="compaction cleared">
      <div class="compaction-rule">
        <span class="compaction-toggle static">{t('conversation cleared')}</span>
        {onReveal && !item.revealed && (
          <button
            type="button"
            class="compaction-reveal"
            disabled={revealing}
            onClick={() => onReveal(item)}
            title={t('These turns were cleared. They are still on disk; show them anyway?')}
          >
            {revealing ? t('loading…') : t('show what was cleared')}
          </button>
        )}
      </div>
    </div>
  )
}
