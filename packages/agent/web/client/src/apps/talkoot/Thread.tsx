import { t, tn } from '../../i18n'
import type { TalkootLine } from '../../platform/ctrlproto/types'
import { lineKey } from '../../platform/talkoot/store'
import { clockTime } from '../../ui/formatting'
import { MarkOf } from '../../ui/talkoot/MemberMark'
import { RoomLine } from '../../ui/talkoot/RoomLine'

// Thread is one chain's teammate messages, folded to a line that names who
// spoke, how many messages there are, and when the newest arrived. It opens in
// place. Teammate traffic is where a chain runs away, so the folded line keeps
// the chain's pause.
export function Thread({
  members,
  lines,
  open,
  onToggle,
  onPauseChain,
}: {
  members: string[]
  lines: TalkootLine[]
  open: boolean
  onToggle: (open: boolean) => void
  onPauseChain?: () => void
}) {
  const newest = lines[lines.length - 1]?.at ?? ''
  return (
    <details class="talkoot-exchange tka-thread" open={open} onToggle={(e) => onToggle((e.currentTarget as HTMLDetailsElement).open)}>
      <summary>
        <span class="tka-thread-marks">
          {members.map((m) => (
            <MarkOf key={m} id={m} />
          ))}
        </span>
        <span class="tka-thread-who">{members.join(', ')}</span>
        <span class="talkoot-note">
          {tn(lines.length, '%d message', '%d messages')} · {t('latest %s', clockTime(newest))}
        </span>
        {onPauseChain && (
          <button
            class="btn sm ghost"
            title={t('Pause the chain this thread belongs to')}
            onClick={(e) => {
              e.preventDefault()
              onPauseChain()
            }}
          >
            ⏸
          </button>
        )}
      </summary>
      {lines.map((l) => (
        <RoomLine key={lineKey(l)} line={l} />
      ))}
    </details>
  )
}
