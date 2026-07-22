import { useEffect, useRef, useState } from 'preact/hooks'
import { t } from '../../i18n'
import type { Group } from '../../platform/ctrlproto/types'

// GroupMenu is the control panel's per-session group control: a small button
// that opens a dropdown of every session group as a toggle, plus "New group".
// app.tsx owns the groups, the membership, and the verbs (per the web layering
// rules); this only renders and emits intent. Membership for THIS session is
// derived from the groups' member lists so it always reflects the latest fetch.
export function GroupMenu(props: {
  sessionId: string
  groups: Group[]
  onToggle: (groupId: string) => void
  onCreate: () => void
}) {
  const [open, setOpen] = useState(false)
  const ref = useRef<HTMLDivElement>(null)

  useEffect(() => {
    if (!open) return
    const onDoc = (e: MouseEvent) => {
      if (ref.current && !ref.current.contains(e.target as Node)) setOpen(false)
    }
    document.addEventListener('mousedown', onDoc)
    return () => document.removeEventListener('mousedown', onDoc)
  }, [open])

  const memberOf = props.groups.filter((g) => g.members.includes(props.sessionId))

  return (
    <div class="groupmenu" ref={ref}>
      <button class="icon sm" title={t('Groups')} onClick={(e) => (e.stopPropagation(), setOpen((v) => !v))}>
        🏷{memberOf.length > 0 ? memberOf.length : ''}
      </button>
      {open && (
        <div class="groupmenu-pop" onClick={(e) => e.stopPropagation()}>
          {props.groups.length === 0 && <div class="groupmenu-empty">{t('No groups yet')}</div>}
          {props.groups.map((g) => {
            const on = g.members.includes(props.sessionId)
            return (
              <button key={g.id} class={`groupmenu-item${on ? ' on' : ''}`} onClick={() => props.onToggle(g.id)}>
                <span class="groupmenu-check">{on ? '✓' : ''}</span>
                {g.color && <span class="groupmenu-dot" style={{ background: g.color }} />}
                <span class="groupmenu-name">{g.name}</span>
              </button>
            )
          })}
          <button class="groupmenu-item groupmenu-new" onClick={() => (setOpen(false), props.onCreate())}>
            + {t('New group')}
          </button>
        </div>
      )}
    </div>
  )
}
