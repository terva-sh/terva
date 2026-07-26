import { t } from '../../i18n'
import { humanGap } from '../../ui/formatting'

// TimeGap marks a silence between two messages — "3h later", centred in the
// stream like the compaction and clear dividers it sits alongside.
//
// It exists because the per-message stamps answer the wrong half of the
// question. They say when each message landed; working out how long you were
// away means subtracting one clock time from another, which is arithmetic, not
// a glance. This is the glance. It appears only above the threshold in
// itemSequence, so a transcript of ordinary back-and-forth carries none at all
// and seeing one still means something.
export function TimeGap({ ms }: { ms: number }) {
  return (
    <div class="time-gap" role="separator">
      <span class="time-gap-label">{t('%s later', humanGap(ms))}</span>
    </div>
  )
}
