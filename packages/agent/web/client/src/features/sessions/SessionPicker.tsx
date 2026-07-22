import { t, tn } from '../../i18n'
import type { Group, SessionInfo } from '../../platform/ctrlproto/types'
import type { GroupFilter } from '../../platform/groups'
import { GroupMenu } from './GroupMenu'
import { GroupFilterBar } from './GroupFilterBar'

export function SessionPicker(props: {
  sessions: SessionInfo[]
  current: string
  onSelect: (id: string) => void
  onNew: () => void
  onRename: (session: SessionInfo) => void
  onGenerateTitle: (session: SessionInfo) => void
  onDelete: (session: SessionInfo) => void
  onClose: () => void
  groups?: Group[]
  filterGroups?: Group[]
  filter?: GroupFilter
  onCycleGroup?: (id: string) => void
  onToggleGroup?: (session: SessionInfo, groupId: string) => void
  onCreateGroup?: (session: SessionInfo) => void
}) {
  const groups = props.groups ?? []
  const filterGroups = props.filterGroups ?? []
  return (
    <div class="drawer-scrim" onClick={props.onClose}>
      <aside class="drawer" onClick={(event) => event.stopPropagation()}>
        <div class="drawer-head">
          <strong>{t('Sessions')}</strong>
          {props.filter && props.onCycleGroup && (
            <GroupFilterBar groups={filterGroups} filter={props.filter} onCycle={props.onCycleGroup} />
          )}
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
              {props.onToggleGroup && props.onCreateGroup && (
                <GroupMenu
                  sessionId={session.id}
                  groups={groups}
                  onToggle={(gid) => props.onToggleGroup!(session, gid)}
                  onCreate={() => props.onCreateGroup!(session)}
                />
              )}
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
