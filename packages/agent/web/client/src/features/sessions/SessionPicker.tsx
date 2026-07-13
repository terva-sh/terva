import { t, tn } from '../../i18n'
import type { SessionInfo } from '../../platform/ctrlproto/types'

export function SessionPicker(props: {
  sessions: SessionInfo[]
  current: string
  onSelect: (id: string) => void
  onNew: () => void
  onRename: (session: SessionInfo) => void
  onGenerateTitle: (session: SessionInfo) => void
  onDelete: (session: SessionInfo) => void
  onClose: () => void
}) {
  return (
    <div class="drawer-scrim" onClick={props.onClose}>
      <aside class="drawer" onClick={(event) => event.stopPropagation()}>
        <div class="drawer-head">
          <strong>{t('Sessions')}</strong>
          <button class="btn" onClick={props.onNew}>
            + {t('New')}
          </button>
        </div>
        <div class="session-list">
          {props.sessions.map((session) => (
            <div
              key={session.id}
              class={`session${session.id === props.current ? ' active' : ''}`}
              onClick={() => props.onSelect(session.id)}
            >
              <div class="session-main">
                <div class="session-title">{session.title || session.id}</div>
                <div class="session-meta">
                  {session.model ? session.model + ' · ' : ''}
                  {tn(session.messages, '%d msg', '%d msgs')}
                  {session.usage?.cost_usd ? ' · $' + session.usage.cost_usd.toFixed(3) : ''}
                </div>
              </div>
              <button
                class="icon sm"
                title={t('Rename')}
                onClick={(event) => (event.stopPropagation(), props.onRename(session))}
              >
                ✎
              </button>
              <button
                class="icon sm"
                title={t('Generate title')}
                onClick={(event) => (event.stopPropagation(), props.onGenerateTitle(session))}
              >
                ✨
              </button>
              <button
                class="icon sm"
                title={t('Delete')}
                onClick={(event) => (event.stopPropagation(), props.onDelete(session))}
              >
                ×
              </button>
            </div>
          ))}
        </div>
      </aside>
    </div>
  )
}
