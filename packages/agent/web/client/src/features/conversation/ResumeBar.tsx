import { t } from '../../i18n'

// ResumeBar offers to run the loop again on a session whose last turn died
// without producing a reply — a provider error, or a daemon that went away
// mid-turn. It is the panel's half of the same offer the TUI makes as a status
// line naming /continue.
//
// It exists because the failure is silent by construction. The transcript is
// complete and correct, nothing is running, and the last thing on screen is the
// user's own message: there is no error state to render, only an absence, and an
// absence looks exactly like a conversation waiting its turn.
//
// `state` is the daemon's answer (SessionInfo.resume), not a guess made here.
// The rule is subtler than a role check — a compaction summary and a cut-short
// reply both hide from one — and working it out client-side would be a second
// implementation free to disagree with the TUI about when to offer this.
//
// Nothing is sent until the button is pressed. A resume costs tokens, so it stays
// the user's decision; this only makes the decision available.
export function ResumeBar({
  state,
  busy,
  onResume,
}: {
  state?: string
  busy?: boolean
  onResume: () => void
}) {
  // Hidden while a turn runs: a turn in flight is itself proof the session is
  // not stuck, and `state` was computed at the last snapshot.
  if (!state || busy) return null
  const cutShort = state === 'after-cut-short'
  return (
    <div class="resume-bar" role="status">
      <span class="resume-why">
        {cutShort ? t('The last reply stopped partway.') : t('The last turn ended without a reply.')}
      </span>
      <button type="button" class="resume-go" onClick={onResume}>
        {cutShort ? t('finish it') : t('ask again')}
      </button>
    </div>
  )
}
