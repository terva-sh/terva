import { t } from '../../i18n'
import type { TalkootWakes } from '../../platform/ctrlproto/types'
import { dayTime } from './RoomLine'

// TeamWakes says whether this team's members may set a wake, with the
// person's caps, and lists the open wakes. A wake starts a member's turn with
// no new post from a person, so the app must show that it is on and what
// waits. User configuration turns it on, and nothing here does. A caller that
// may steer passes onCancel, and each open wake then has a cancel button.
//
// The follow-up of a routine's run is in the same list. It starts one more
// run of the routine and no turn of the member, so its line says so.
//
// A wake that came due behind a pause is in the list too, with the time that
// it fired. It waits for a resume, and a person can still cancel it.
export function TeamWakes({ wakes, onCancel }: { wakes?: TalkootWakes; onCancel?: (wake: string) => void }) {
  if (!wakes) return null
  const open = wakes.open ?? []
  return (
    <div class="talkoot-wakes" data-on={wakes.on ? 'true' : 'false'}>
      <div class="talkoot-note">
        {wakes.on
          ? t('Wakes on: a member may hold %d, each at most %s ahead', wakes.per_member ?? 0, wakes.max_delay ?? '')
          : t('Wakes off')}
      </div>
      {wakes.problem && <div class="talkoot-error">{wakes.problem}</div>}
      {!wakes.on && open.length > 0 && <div class="talkoot-note">{t('An open wake does not fire while wakes are off.')}</div>}
      {open.length > 0 && (
        <ul class="talkoot-held">
          {open.map((w) => (
            <li key={w.id}>
              <span class="talkoot-held-why">{dayTime(w.due)}</span>{' '}
              {w.held
                ? t('%s, came due and waits for a resume: %s', w.member, w.reason)
                : w.routine
                  ? t('%s, a follow-up of its routine: %s', w.member, w.reason)
                  : `${w.member}: ${w.reason}`}
              {onCancel && (
                <button class="btn sm ghost" title={t('Cancel this wake')} aria-label={t('Cancel the wake of %s', w.member)} onClick={() => onCancel(w.id)}>
                  ✕
                </button>
              )}
            </li>
          ))}
        </ul>
      )}
    </div>
  )
}
