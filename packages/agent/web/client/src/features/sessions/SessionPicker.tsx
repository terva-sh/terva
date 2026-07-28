import { t, tn } from '../../i18n'
import type { ArchivedSessionInfo, Group, SessionInfo } from '../../platform/ctrlproto/types'
import type { GroupFilter } from '../../platform/groups'
import { sectionSessions } from '../../platform/sessionstatus'
import { humanBytes } from '../../ui/formatting'
import { GroupMenu } from './GroupMenu'
import { GroupFilterBar } from './GroupFilterBar'
import { SessionSection, useColdGroup } from './SessionSection'

export function SessionPicker(props: {
  sessions: SessionInfo[]
  current: string
  // liveBusy[id], when set, is the authoritative turn-in-flight state from a
  // live subscription — same map the board reads, and being present in it at
  // all also proves the session is live. See platform/sessionstatus.
  liveBusy?: Record<string, boolean>
  onSelect: (id: string) => void
  onNew: () => void
  // Leave the focused session for the landing. Absent when there is no session
  // to leave (opened from the landing itself), in which case the row is hidden.
  onGoLanding?: () => void
  onRename: (session: SessionInfo) => void
  onGenerateTitle: (session: SessionInfo) => void
  onDelete: (session: SessionInfo) => void
  // Archiving is the middle verb: the session leaves this list without leaving
  // the disk. Optional, so a panel served by a daemon that does not offer it
  // renders no control rather than one that fails.
  onArchive?: (session: SessionInfo) => void
  // The archive browser. archived is null until it has been fetched once, which
  // is what lets the toggle say "loading" rather than "empty".
  archived?: ArchivedSessionInfo[] | null
  showArchived?: boolean
  onToggleArchived?: () => void
  onRestore?: (id: string) => void
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
  // Recomputed every render so a session ages out of the idle window (and moves
  // between groups on the 4s re-list) without anything else having to notice.
  const sections = sectionSessions(props.sessions, props.liveBusy, Date.now())
  // The drawer unmounts when it closes, so the seed happens each time it opens:
  // collapsed, unless cold is all there is.
  const [coldOpen, toggleCold] = useColdGroup(sections)

  // One row, rendered into whichever group its session landed in. No status
  // badge: the section header already says it, and this row is 320px wide
  // carrying five controls already.
  const row = (session: SessionInfo) => (
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
      {props.onArchive && (
        <button
          class="icon sm"
          title={t('Archive')}
          onClick={(event) => (event.stopPropagation(), props.onArchive!(session))}
        >
          ⤓
        </button>
      )}
      <button
        class="icon sm"
        title={t('Delete')}
        onClick={(event) => (event.stopPropagation(), props.onDelete(session))}
      >
        ×
      </button>
    </div>
  )

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
        {props.onGoLanding && (
          <button class="drawer-home" onClick={props.onGoLanding}>
            ← {t('Back to landing')}
          </button>
        )}
        <div class="session-list">
          <SessionSection status="busy" label={t('busy')} count={sections.busy.length}>
            {sections.busy.map(row)}
          </SessionSection>
          <SessionSection status="idle" label={t('idle')} count={sections.idle.length}>
            {sections.idle.map(row)}
          </SessionSection>
          <SessionSection
            status="cold"
            label={t('cold')}
            count={sections.cold.length}
            open={coldOpen}
            onToggle={toggleCold}
          >
            {sections.cold.map(row)}
          </SessionSection>
        </div>
        {props.onToggleArchived && (
          <div class="drawer-archive">
            <button class="drawer-archive__toggle" onClick={props.onToggleArchived}>
              {props.showArchived ? t('Hide archived') : archiveLabel(props.archived)}
            </button>
            {props.showArchived && props.archived && props.archived.length === 0 && (
              <div class="drawer-archive__empty">{t('Nothing archived for this directory.')}</div>
            )}
            {props.showArchived &&
              (props.archived ?? []).map((a) => (
                <div class="session archived" key={a.id}>
                  <div class="session-main">
                    <div class="session-title">{a.title || a.preview || t('(untitled)')}</div>
                    <div class="session-meta">
                      {a.model ? a.model + ' · ' : ''}
                      {tn(a.message_count ?? 0, '%d msg', '%d msgs')}
                      {a.bytes ? ' · ' + humanBytes(a.bytes) : ''}
                    </div>
                  </div>
                  {props.onRestore && (
                    <button class="drawer-archive__restore" title={t('Restore')} onClick={() => props.onRestore!(a.id)}>
                      {t('Restore')}
                    </button>
                  )}
                </div>
              ))}
          </div>
        )}
      </aside>
    </div>
  )
}

// archiveLabel counts what is in the archive when that is known. Before the
// first fetch it cannot, and claiming "Archived (0)" would be a lie the user
// would act on.
export function archiveLabel(archived: ArchivedSessionInfo[] | null | undefined): string {
  if (archived == null) return t('Archived')
  return t('Archived (%s)', String(archived.length))
}

