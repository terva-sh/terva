import { t, tn } from '../../i18n'
import type { Group, SessionInfo } from '../../platform/ctrlproto/types'
import type { GroupFilter } from '../../platform/groups'
import { sectionSessions } from '../../platform/sessionstatus'
import { Placeholder } from '../../ui/Loading'
import { GroupMenu } from '../sessions/GroupMenu'
import { GroupFilterBar } from '../sessions/GroupFilterBar'
import { SessionSection, useColdGroup } from '../sessions/SessionSection'

// SessionsBoard is the monitor view over N sessions: one tile per session, live
// status at a glance, click a tile to focus it. Pure presentation — app.tsx
// owns the list, the subscriptions, and the verbs (per the web layering rules);
// this renders SessionInfo[] and emits intent through callbacks. The tile's
// status comes straight from SessionInfo.busy/live, which sessions.list carries
// (orchestration frontend stage 4.1; docs/proposals/orchestration-frontend.md).
export function SessionsBoard(props: {
  sessions: SessionInfo[]
  // Whether sessions.list has ANSWERED. An empty list means two different
  // things, and this is the only thing that tells them apart: before the first
  // answer the board showed "No sessions in this workspace yet." off a useState
  // default, so a panel that had merely finished painting asserted an empty
  // workspace to anyone who opened it faster than the socket connected.
  // Optional so a caller that has no such flag (a test double, an embedding)
  // keeps the old behaviour rather than being stuck on a placeholder forever.
  loaded?: boolean
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
  // Archiving: the session leaves the list without leaving the disk. Optional,
  // so a surface served by a daemon that does not offer it renders no control.
  onArchive?: (session: SessionInfo) => void
  // Session groups (optional; absent on a daemon that doesn't serve them).
  // `groups` (user groups) feeds the per-tile assign menu; `filterGroups` (user
  // groups + the derived `stage` chip) feeds the include/exclude filter bar,
  // which app.tsx applies to the sessions prop.
  groups?: Group[]
  filterGroups?: Group[]
  filter?: GroupFilter
  onCycleGroup?: (id: string) => void
  onToggleGroup?: (session: SessionInfo, groupId: string) => void
  onCreateGroup?: (session: SessionInfo) => void
}) {
  const groups = props.groups ?? []
  const filterGroups = props.filterGroups ?? []
  const sections = sectionSessions(props.sessions, props.liveBusy, Date.now())
  // Unlike the drawer the board stays mounted while you are on it, so an
  // expanded cold group has to survive the 4s re-list rather than snapping shut
  // under the reader every four seconds.
  const [coldOpen, toggleCold] = useColdGroup(sections)

  // No per-tile status pill. The group header above the tile already says the
  // word, and saying it twice on every tile was the loudest thing on a screen
  // whose whole job is to let you find the one session that is moving.
  const tile = (s: SessionInfo) => {
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
          <button class="icon sm" title={t('Rename')} onClick={(e) => (e.stopPropagation(), props.onRename(s))}>
            ✎
          </button>
          {props.onArchive && (
            <button class="icon sm" title={t('Archive')} onClick={(e) => (e.stopPropagation(), props.onArchive!(s))}>
              ⤓
            </button>
          )}
          <button class="icon sm" title={t('Delete')} onClick={(e) => (e.stopPropagation(), props.onDelete(s))}>
            ×
          </button>
        </div>
      </div>
    )
  }

  return (
    <div class="board">
      <div class="board-head">
        <strong>{t('Sessions')}</strong>
        {props.filter && props.onCycleGroup && (
          <GroupFilterBar groups={filterGroups} filter={props.filter} onCycle={props.onCycleGroup} />
        )}
        <button class="btn" onClick={props.onNew}>
          + {t('New')}
        </button>
      </div>
      {props.sessions.length === 0 && props.loaded === false ? (
        <Placeholder label={t('Loading sessions…')} rows={2} />
      ) : props.sessions.length === 0 ? (
        <div class="board-empty">{t('No sessions in this workspace yet.')}</div>
      ) : (
        <>
          <SessionSection status="busy" label={t('busy')} count={sections.busy.length}>
            <div class="board-grid">{sections.busy.map(tile)}</div>
          </SessionSection>
          <SessionSection status="idle" label={t('idle')} count={sections.idle.length}>
            <div class="board-grid">{sections.idle.map(tile)}</div>
          </SessionSection>
          <SessionSection
            status="cold"
            label={t('cold')}
            count={sections.cold.length}
            open={coldOpen}
            onToggle={toggleCold}
          >
            <div class="board-grid">{sections.cold.map(tile)}</div>
          </SessionSection>
        </>
      )}
    </div>
  )
}
