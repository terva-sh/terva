import { t, tn } from '../../i18n'
import type { Group, SessionInfo } from '../../platform/ctrlproto/types'
import { GroupMenu } from '../sessions/GroupMenu'

// SessionsBoard is the monitor view over N sessions: one tile per session, live
// status at a glance, click a tile to focus it. Pure presentation — app.tsx
// owns the list, the subscriptions, and the verbs (per the web layering rules);
// this renders SessionInfo[] and emits intent through callbacks. The tile's
// status comes straight from SessionInfo.busy/live, which sessions.list carries
// (orchestration frontend stage 4.1; docs/proposals/orchestration-frontend.md).
export function SessionsBoard(props: {
  sessions: SessionInfo[]
  current: string
  // liveBusy[id], when set, is the authoritative turn-in-flight state from a
  // live subscription (phase B) — it flips at turn latency and wins over the
  // point-in-time SessionInfo.busy the periodic list carries. A subscribed tile
  // is by definition live. Absent id → fall back to the list's flags.
  liveBusy?: Record<string, boolean>
  onSelect: (id: string) => void
  onNew: () => void
  onRename: (session: SessionInfo) => void
  onDelete: (session: SessionInfo) => void
  // Session groups (optional; absent on a daemon that doesn't serve them). The
  // filter narrows the board (app.tsx applies it to the sessions prop); the
  // per-tile menu files a session in or out of a group.
  groups?: Group[]
  groupFilter?: string
  onGroupFilter?: (id: string) => void
  onToggleGroup?: (session: SessionInfo, groupId: string) => void
  onCreateGroup?: (session: SessionInfo) => void
}) {
  const groups = props.groups ?? []
  return (
    <div class="board">
      <div class="board-head">
        <strong>{t('Sessions')}</strong>
        {groups.length > 0 && props.onGroupFilter && (
          <select
            class="board-groupfilter"
            value={props.groupFilter ?? ''}
            onChange={(e) => props.onGroupFilter!((e.target as HTMLSelectElement).value)}
            title={t('Show only sessions in a group')}
          >
            <option value="">{t('All groups')}</option>
            {groups.map((g) => (
              <option key={g.id} value={g.id}>
                {g.name} ({g.members.length})
              </option>
            ))}
          </select>
        )}
        <button class="btn" onClick={props.onNew}>
          + {t('New')}
        </button>
      </div>
      {props.sessions.length === 0 ? (
        <div class="board-empty">{t('No sessions in this workspace yet.')}</div>
      ) : (
        <div class="board-grid">
          {props.sessions.map((s) => {
            // A subscribed tile's busy comes from its stream (authoritative, at
            // turn latency); an unsubscribed one falls back to the list's
            // point-in-time flag. Either way it's one value per tile — never
            // mixed — so tiles don't flicker between two truths.
            const streamed = props.liveBusy?.[s.id]
            const busy = streamed ?? !!s.busy
            const live = streamed !== undefined || !!s.live
            const status = busy ? 'busy' : live ? 'live' : 'cold'
            const label = busy ? t('busy') : live ? t('idle') : t('cold')
            return (
              <div
                key={s.id}
                class={`board-tile${s.id === props.current ? ' active' : ''}`}
                role="button"
                tabIndex={0}
                onClick={() => props.onSelect(s.id)}
                onKeyDown={(e) => {
                  if (e.key === 'Enter' || e.key === ' ') {
                    e.preventDefault()
                    props.onSelect(s.id)
                  }
                }}
              >
                <div class="board-tile-head">
                  <span class="board-tile-title">{s.title || s.id}</span>
                  <span class={`board-status ${status}`}>{label}</span>
                </div>
                <div class="board-tile-meta">
                  {s.model ? s.model + ' · ' : ''}
                  {tn(s.messages, '%d msg', '%d msgs')}
                  {s.usage?.cost_usd ? ' · $' + s.usage.cost_usd.toFixed(3) : ''}
                </div>
                <div class="board-tile-actions">
                  {props.onToggleGroup && props.onCreateGroup && (
                    <GroupMenu
                      sessionId={s.id}
                      groups={groups}
                      onToggle={(gid) => props.onToggleGroup!(s, gid)}
                      onCreate={() => props.onCreateGroup!(s)}
                    />
                  )}
                  <button
                    class="icon sm"
                    title={t('Rename')}
                    onClick={(e) => (e.stopPropagation(), props.onRename(s))}
                  >
                    ✎
                  </button>
                  <button
                    class="icon sm"
                    title={t('Delete')}
                    onClick={(e) => (e.stopPropagation(), props.onDelete(s))}
                  >
                    ×
                  </button>
                </div>
              </div>
            )
          })}
        </div>
      )}
    </div>
  )
}
