import { useState } from 'preact/hooks'
import { t } from '../../i18n'
import type { ClientLike } from '../../platform/ctrlproto/client'
import type { SessionInfo } from '../../platform/ctrlproto/types'
import { panelHref } from '../navlinks'

// TeamManager opens the daemon's team manager (talkoot.manager). The manager is
// one Hautoja session outside every team. It reads every team and proposes
// changes to any of them, and each proposal arrives as a card in that team.
//
// The daemon keeps one manager, so every click opens the same conversation.
// 🔑 A link the person clicks, and not window.open after the round trip: a
// browser blocks a window that no click opened.
export function TeamManager({ client, primary = false }: { client: ClientLike; primary?: boolean }) {
  const [session, setSession] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const start = () => {
    if (busy) return
    setBusy(true)
    void client
      .send<{ session: SessionInfo }>('talkoot.manager', null)
      .then(
        (r) => {
          const sid = r?.session?.id ?? ''
          setError(sid ? '' : t('The daemon opened no team manager session.'))
          setSession(sid)
        },
        (e: unknown) => setError(e instanceof Error ? e.message : String(e)),
      )
      .finally(() => setBusy(false))
  }
  return (
    <span class="talkoot-manager">
      {session ? (
        <a class={`btn sm${primary ? ' primary' : ''}`} href={panelHref(session)} target="_blank" rel="noopener">
          {t('Open the team manager')}
        </a>
      ) : (
        <button
          class={`btn sm${primary ? ' primary' : ' ghost'}`}
          disabled={busy}
          onClick={start}
          title={t('The team manager reads every team and proposes changes for you to approve.')}
        >
          {t('Team manager')}
        </button>
      )}
      {error && <span class="talkoot-error">{error}</span>}
    </span>
  )
}
