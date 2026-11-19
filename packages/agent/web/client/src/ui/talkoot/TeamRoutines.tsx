import { t } from '../../i18n'
import type { TalkootRoutine, TalkootRoutines } from '../../platform/ctrlproto/types'
import { dayTime, who } from './RoomLine'
import { kindName, limitLabel, limitRows, reviewLimitNote, routineText, usdText } from './routines'

type Action = 'pause' | 'resume' | 'delete'

// TeamRoutines says whether this team's members may ask for a routine, with
// the person's cap, and lists every routine with its state. A routine starts
// work with no person present, so the app must show that it is on and what
// is stored. User configuration turns it on, and nothing here does. A caller
// that may steer passes onSet, and each routine then has its buttons.
export function TeamRoutines({ routines, onSet }: { routines?: TalkootRoutines; onSet?: (routine: string, action: Action) => void }) {
  if (!routines) return null
  const list = routines.routines ?? []
  return (
    <div class="talkoot-routines" data-on={routines.on ? 'true' : 'false'}>
      <div class="talkoot-note">{routines.on ? t('Routines on: the team may hold %d', routines.max ?? 0) : t('Routines off')}</div>
      {routines.problem && <div class="talkoot-error">{routines.problem}</div>}
      {/* Routines on changes what two postures ask for. A person who set a
          member to ask or auto-edit must see that here. */}
      {routines.on && (
        <div class="talkoot-note authority">
          {t('While routines are on, a member asks for a routine, and writes to you, with no approval card.')}
        </div>
      )}
      {!routines.on && list.length > 0 && <div class="talkoot-note">{t('A routine does not resume while routines are off.')}</div>}
      {/* A person who reads the limits below must know which of them the
          daemon keeps. */}
      {list.length > 0 && (
        <div class="talkoot-note">{t('A routine that passes its daily spend limit pauses, and a run is stopped when its cost passes that limit.')}</div>
      )}
      {list.length > 0 && <div class={routines.reviews_counted ? 'talkoot-note' : 'talkoot-note authority'}>{reviewLimitNote(routines.reviews_counted)}</div>}
      {list.length > 0 && (
        <ul class="talkoot-routine-list">
          {list.map((r) => (
            <Routine key={r.id} routine={r} counted={routines.reviews_counted} onSet={onSet} />
          ))}
        </ul>
      )}
    </div>
  )
}

function Routine({ routine: r, counted, onSet }: { routine: TalkootRoutine; counted?: boolean; onSet?: (routine: string, action: Action) => void }) {
  // A record that does not read has only its id and the problem. A delete
  // clears it.
  if (!r.kind) {
    return (
      <li>
        <div class="talkoot-error">{r.problem}</div>
        {onSet && <Delete routine={r} onSet={onSet} />}
      </li>
    )
  }
  const paused = r.status === 'paused'
  // An ended routine reached the end that its approval gave it. It does not
  // pause and does not resume, so it has the delete button alone.
  const ended = r.status === 'ended'
  return (
    <li data-status={r.status}>
      <details>
        <summary>
          <span class="talkoot-kind">{kindName(r.kind)}</span> {r.member}: {r.schedule}{' '}
          <span class="talkoot-held-why">
            {ended ? t('ended') : paused ? t('paused') : r.run_session ? t('a run is at work') : r.next ? t('next %s', dayTime(r.next)) : ''}
          </span>
        </summary>
        <pre class="preview">{routineText(r)}</pre>
        <table class="talkoot-fields">
          <tbody>
            {limitRows(r.fields).map((row) => (
              <tr key={row.field}>
                <th>{limitLabel(row.field)}</th>
                <td>{row.value}</td>
              </tr>
            ))}
          </tbody>
        </table>
        {r.ends_at && <div class="talkoot-note">{t('Ends %s', dayTime(r.ends_at))}</div>}
        {r.approved_by && <div class="talkoot-note">{t('Approved by %s, %s', who(r.approved_by), dayTime(r.approved_at))}</div>}
        <div class="talkoot-note">{t('Runs started: %d', r.runs ?? 0)}</div>
        {r.kind !== 'watcher' && (
          <div class="talkoot-note">{t('Spent today: $%s of $%s', usdText(r.spent_today_usd ?? 0), usdText(r.fields.spend_usd_per_day))}</div>
        )}
        {/* A count that nothing keeps would read as no request today. */}
        {r.kind !== 'watcher' && counted && (
          <div class="talkoot-note">{t('Reviews asked for today: %d of %d', r.reviews_today ?? 0, r.fields.reviews_per_day)}</div>
        )}
        {paused && r.changed_by && (
          <div class="talkoot-note">{r.reason ? t('Paused by %s: %s', who(r.changed_by), r.reason) : t('Paused by %s', who(r.changed_by))}</div>
        )}
      </details>
      {/* terva's own pause names no person. It follows a failed run, so the
          cause shows without a click. */}
      {paused && !r.changed_by && r.reason && <div class="talkoot-error">{t('Paused: %s', r.reason)}</div>}
      {ended && <div class="talkoot-note">{r.reason ? t('Ended: %s', r.reason) : t('Ended')}</div>}
      {r.problem && <div class="talkoot-error">{r.problem}</div>}
      {onSet && (
        <div class="talkoot-routine-actions">
          {ended ? null : paused ? (
            <button class="btn sm" aria-label={t('Resume the routine of %s', r.member)} onClick={() => onSet(r.id, 'resume')}>
              {t('Resume')}
            </button>
          ) : (
            <button class="btn sm" aria-label={t('Pause the routine of %s', r.member)} onClick={() => onSet(r.id, 'pause')}>
              {t('Pause')}
            </button>
          )}
          <Delete routine={r} onSet={onSet} />
        </div>
      )}
    </li>
  )
}

// Delete removes a routine for good, so it asks first. The member must ask
// again, and a person must approve again, to get it back.
function Delete({ routine: r, onSet }: { routine: TalkootRoutine; onSet: (routine: string, action: Action) => void }) {
  return (
    <button
      class="btn sm ghost"
      aria-label={r.member ? t('Delete the routine of %s', r.member) : t('Delete the routine')}
      onClick={() => {
        if (window.confirm(t('Delete this routine? A member must ask again, and you must approve again, to get it back.'))) onSet(r.id, 'delete')
      }}
    >
      {t('Delete')}
    </button>
  )
}
