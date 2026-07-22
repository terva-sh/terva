import { t } from '../../i18n'
import type { Group } from '../../platform/ctrlproto/types'
import { groupState, type GroupFilter } from '../../platform/groups'

// A row of tri-state group chips for the board/picker head. Tapping a chip cycles
// its filter state off → show-only → hide → off. A system chip (the derived
// `stage` bucket) reads italic; it filters like any other but can't be edited.
// The panel files sessions into groups per-session (GroupMenu), so a chip here is
// filter-only. app.tsx owns the filter state and applies it to the sessions prop.
export function GroupFilterBar(props: {
  groups: Group[]
  filter: GroupFilter
  onCycle: (id: string) => void
}) {
  if (props.groups.length === 0) return null
  return (
    <div class="groupfilter" role="group" aria-label={t('Filter sessions by group')}>
      {props.groups.map((g) => {
        const state = groupState(props.filter, g.id)
        const title =
          state === 'off'
            ? t('Show only “%s”', g.name)
            : state === 'include'
              ? t('Hide “%s”', g.name)
              : t('Clear the filter on “%s”', g.name)
        return (
          <button
            key={g.id}
            class="groupfilter__chip"
            data-state={state}
            data-system={g.system ? '' : undefined}
            aria-pressed={state !== 'off'}
            onClick={() => props.onCycle(g.id)}
            title={title}
          >
            {g.color && <span class="groupfilter__dot" style={{ background: g.color }} aria-hidden="true" />}
            <span class="groupfilter__name">{g.name}</span>
            <span class="groupfilter__count">{g.members.length}</span>
          </button>
        )
      })}
    </div>
  )
}
