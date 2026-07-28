import type { ComponentChildren } from 'preact'
import { useRef, useState } from 'preact/hooks'
import { coldStartsOpen, type Sections, type SessionStatus } from '../../platform/sessionstatus'

// useColdGroup owns whether the cold group is open, for whichever surface is
// rendering it.
//
// ⚠️ Seeded from the first sectioning that HAS a list — NOT from the first
// render. sessions.list is a round trip after the hello, so at mount the list is
// not empty, it is UNANSWERED. Seeding there asked coldStartsOpen "is cold all
// there is?" about a list nobody had asked for yet, got a vacuous yes, and the
// group opened on every load — the same three-states-not-two trap that once put
// "No sessions in this workspace yet." under a connecting socket.
//
// After that first answer the state is the USER's. Re-deriving it each render
// would snap the group shut under the reader the moment anything went busy.
export function useColdGroup(sections: Sections): [boolean, () => void] {
  const seeded = useRef<boolean | null>(null)
  const [chosen, setChosen] = useState<boolean | null>(null)
  if (seeded.current === null && sections.busy.length + sections.idle.length + sections.cold.length > 0) {
    seeded.current = coldStartsOpen(sections)
  }
  const open = chosen ?? seeded.current ?? false
  return [open, () => setChosen(!open)]
}

// One busy/idle/cold group's header and its contents, shared by the drawer's
// list and the board's grid so the two cannot drift into different chrome for
// the same idea. It lives here rather than in ui/ because it is PANEL-only —
// ui/ primitives are styled in the shared ui.css that Stage also imports, and a
// rule only one app can use has no business in that base. The board already
// reaches across for GroupMenu, so the direction is established.
//
// An empty group renders nothing at all: a header over no rows is a section
// that claims to be a place you can look.
//
// Collapsibility is implied by onToggle. Only the cold group passes one — busy
// and idle are the reason the drawer was opened, and a disclosure on each would
// be three controls where one is useful.
export function SessionSection(props: {
  status: SessionStatus
  label: string
  count: number
  open?: boolean
  onToggle?: () => void
  children: ComponentChildren
}) {
  if (props.count === 0) return null
  const collapsible = props.onToggle !== undefined
  const open = !collapsible || props.open
  // The caret slot is rendered by EVERY header, empty where there is nothing to
  // toggle. Without it the one collapsible header indents its label past the
  // others and the group titles read as a ragged edge.
  const inner = (
    <>
      <span class="session-section__caret" aria-hidden="true">
        {collapsible ? (open ? '▼' : '▶') : ''}
      </span>
      <span class="session-section__label">{props.label}</span>
      <span class="session-section__count">{props.count}</span>
    </>
  )
  return (
    <section class={`session-section session-section--${props.status}`}>
      {collapsible ? (
        <button class="session-section__head session-section__head--toggle" aria-expanded={open} onClick={props.onToggle}>
          {inner}
        </button>
      ) : (
        <div class="session-section__head">{inner}</div>
      )}
      {open && props.children}
    </section>
  )
}
