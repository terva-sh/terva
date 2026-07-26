import { useEffect, useLayoutEffect, useRef, useState } from 'preact/hooks'
import { t } from '../../i18n'
import type { Group } from '../../platform/ctrlproto/types'

// A one-dimensional span in viewport coordinates — what nudge compares.
type Span = { left: number; right: number }

// nudge returns the horizontal shift, in px, that brings `box` back inside
// `limit` with `margin` to spare, or 0 when it already fits.
//
// The menu hangs to the LEFT of its button (right-aligned, so it doesn't push
// past the tile's right edge), and the button is not the leftmost thing in its
// row — so on the leftmost tile of a grid the menu reaches past the container
// and gets cut off. A box wider than the limit is pinned to the left edge:
// clamping the left first means it is the START of each group name that
// survives, which is the half worth reading.
export function nudge(box: Span, limit: Span, margin = 8): number {
  if (box.left < limit.left + margin) return limit.left + margin - box.left
  if (box.right > limit.right - margin) return limit.right - margin - box.right
  return 0
}

// clipBounds is the span the menu has to stay inside: the nearest ancestor that
// does not let content overflow, else the viewport.
//
// ⚠️ Scrolling columns clip SIDEWAYS whether they meant to or not — `overflow-y:
// auto` alone computes overflow-x from `visible` to `auto` (CSS Overflow §3).
// Both hosts sit in one (`.landing`, `.session-list`), which is why the menu
// vanished at the edge rather than merely overhanging.
function clipBounds(el: HTMLElement): Span {
  for (let p = el.parentElement; p; p = p.parentElement) {
    const cs = getComputedStyle(p)
    if (cs.overflowX !== 'visible' || cs.overflowY !== 'visible') {
      const r = p.getBoundingClientRect()
      return { left: r.left, right: r.right }
    }
  }
  return { left: 0, right: window.innerWidth }
}

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
  const popRef = useRef<HTMLDivElement>(null)

  // Placed before paint, so the menu never appears at the clipped position and
  // then jumps. Re-run on the group count: it sets the menu's height, and a
  // long name its width.
  useLayoutEffect(() => {
    const pop = popRef.current
    if (!open || !pop) return
    pop.style.transform = ''
    const dx = nudge(pop.getBoundingClientRect(), clipBounds(pop))
    pop.style.transform = dx ? `translateX(${dx}px)` : ''
  }, [open, props.groups.length])

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
        <div class="groupmenu-pop" ref={popRef} onClick={(e) => e.stopPropagation()}>
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
