import { useEffect, useState } from 'preact/hooks'
import { t } from '../../i18n'
import type { ClientLike } from '../../platform/ctrlproto/client'
import type { SessionInfo } from '../../platform/ctrlproto/types'
import { panelHref } from '../navlinks'

// Recruit opens a recruiter session for the open team (talkoot.recruit). The
// recruiter is a Hautoja session that holds no seat. It reads the roster, asks
// about the job, and proposes a member. Its proposal arrives in this team's
// conversation as a card, which the person approves or declines.
//
// The app has no chat of its own, so the session opens in the control panel.
// 🔑 A link the person clicks, and not window.open after the round trip: a
// browser blocks a window that no click opened.
export function Recruit({ client, id, onError }: { client: ClientLike; id: string; onError: (error: string) => void }) {
  const [session, setSession] = useState('')
  const [busy, setBusy] = useState(false)
  // A recruiter belongs to one team. Another team starts from the button.
  useEffect(() => setSession(''), [id])
  const start = () => {
    if (busy) return
    setBusy(true)
    void client
      .send<{ session: SessionInfo }>('talkoot.recruit', { id })
      .then(
        (r) => {
          const sid = r?.session?.id ?? ''
          onError(sid ? '' : t('The daemon opened no recruiter session.'))
          setSession(sid)
        },
        (e: unknown) => onError(e instanceof Error ? e.message : String(e)),
      )
      .finally(() => setBusy(false))
  }
  if (session) {
    return (
      <div class="talkoot-recruit">
        <a class="btn sm primary" href={panelHref(session)} target="_blank" rel="noopener">
          {t('Open the recruiter')}
        </a>
        <span class="talkoot-note">{t('Its proposal waits here as a card.')}</span>
      </div>
    )
  }
  return (
    <div class="talkoot-recruit">
      <button class="btn sm" disabled={busy} onClick={start} title={t('A recruiter asks about the job and proposes a member for you to approve.')}>
        {t('Find a member')}
      </button>
    </div>
  )
}
