import { useEffect, useLayoutEffect, useMemo, useRef, useState } from 'preact/hooks'
import { t, tn } from '../../i18n'
import type { SessionInfo } from '../../platform/ctrlproto/types'
import { filterSessions, sessionLabel, type PaletteRow } from '../../platform/sessionfilter'
import { sessionStatus } from '../../platform/sessionstatus'
import { useComposition } from '../../ui/composition'
import { humanGap } from '../../ui/formatting'

// The session quick switcher: a palette that jumps to any session without
// leaving the one in front of you. The board stays the full view of every
// session; this is the fast route for a person who knows which one they want.
//
// The input keeps focus the whole time and points at the selected row with
// aria-activedescendant, the combobox pattern. A row never takes focus, so
// typing goes on working after an arrow key.

// isApple reads the platform once. Mod+K is Cmd+K on a Mac and Ctrl+K
// elsewhere (D7 in docs/proposals/web-hub.md): in a Mac text field Ctrl+K
// deletes to the end of the line, and the palette must not take that.
function isApple(): boolean {
  const nav = typeof navigator === 'undefined' ? null : navigator
  return /Mac|iPhone|iPad|iPod/.test(nav?.platform || nav?.userAgent || '')
}

// isPaletteShortcut answers whether a key event is Mod+K.
export function isPaletteShortcut(e: KeyboardEvent, apple = isApple()): boolean {
  if (e.key.toLowerCase() !== 'k' || e.altKey || e.shiftKey) return false
  return apple ? e.metaKey && !e.ctrlKey : e.ctrlKey && !e.metaKey
}

// anotherDialogOpen answers whether a different dialog owns the screen: the
// model picker, the reasoning picker, a landing sheet, the Raati theater. Mod+K
// stands down then. A palette over another modal would stack two modal layers,
// and Escape would close the one on top while the person meant the other.
// The palette's own scrim is left out, so a second Mod+K still closes it.
export function anotherDialogOpen(doc: Document = document): boolean {
  return !!doc.querySelector('.modal-scrim:not(.spal-scrim), .landing-scrim, [aria-modal="true"]:not(.spal)')
}

// paletteShortcutKeys names the two keys for the hint, in the platform's terms.
export function paletteShortcutKeys(apple = isApple()): [string, string] {
  return [apple ? '⌘' : 'Ctrl', 'K']
}

let seq = 0

export function SessionPalette(props: {
  sessions: SessionInfo[]
  current: string
  // liveBusy[id] is the turn-in-flight state from a live subscription, the
  // same map the board and the drawer read (platform/sessionstatus).
  liveBusy?: Record<string, boolean>
  onSelect: (id: string) => void
  onClose: () => void
  // Leave for the board, which shows every session at once. Absent hides it.
  onOpenBoard?: () => void
}) {
  const [query, setQuery] = useState('')
  // The selection is held by session id, not by index: the list re-arrives
  // every few seconds while the palette is open, and an index would move the
  // selection to whichever session slid into its place.
  const [selId, setSelId] = useState('')
  const inputRef = useRef<HTMLInputElement>(null)
  const isComposing = useComposition(inputRef)
  const boardRef = useRef<HTMLButtonElement>(null)
  const base = useMemo(() => `spal-${++seq}`, [])

  const list = useMemo(() => filterSessions(props.sessions, query), [props.sessions, query])
  const rows = [...list.waiting, ...list.recent]
  // The default is the first row that is not the session on screen: the
  // person opened the palette to go somewhere else.
  let idx = rows.findIndex((r) => r.session.id === selId)
  if (idx < 0) idx = Math.max(0, rows.findIndex((r) => r.session.id !== props.current))
  const optId = (i: number) => `${base}-o${i}`

  // ⚠️ A layout effect, not useEffect. Preact runs a component's pending
  // effects early when it renders again before they flush, and that happens
  // before the commit sets refs. The panel re-renders the palette on the
  // sessions.list it sends as the palette opens, so a plain effect found
  // inputRef empty in about one smoke run in ten, and focus stayed in the
  // composer behind the dialog. A layout effect runs at commit, after refs.
  useLayoutEffect(() => {
    // Give focus back to whatever held it, so Escape lands where Mod+K began.
    const prev = document.activeElement as HTMLElement | null
    inputRef.current?.focus()
    return () => prev?.focus?.()
  }, [])

  useEffect(() => {
    document.getElementById(optId(idx))?.scrollIntoView?.({ block: 'nearest' })
  }, [idx, rows.length])

  const pick = (id: string) => {
    if (id === props.current) props.onClose()
    else props.onSelect(id)
  }

  const move = (by: number) => {
    if (rows.length === 0) return
    setSelId(rows[(idx + by + rows.length) % rows.length].session.id)
  }

  // The list keys belong to the input alone. On the dialog they would also
  // catch Enter on the focused board button and open a session instead.
  const onInputKeyDown = (e: KeyboardEvent) => {
    // An IME uses Enter to commit the text it is composing, and the arrows to
    // pick a candidate. Those keys are its until the composition ends.
    if (isComposing(e)) return
    switch (e.key) {
      case 'ArrowDown':
        e.preventDefault()
        move(1)
        return
      case 'ArrowUp':
        e.preventDefault()
        move(-1)
        return
      case 'Enter':
        e.preventDefault()
        if (rows[idx]) pick(rows[idx].session.id)
        return
    }
  }

  const onKeyDown = (e: KeyboardEvent) => {
    switch (e.key) {
      case 'Escape':
        // Stopped here, so a panel-level Escape (the pane rail, the drawer)
        // does not also close under the palette.
        e.preventDefault()
        e.stopPropagation()
        props.onClose()
        return
      case 'Tab': {
        // aria-modal promises focus stays inside. Two stops: the input and
        // the board button.
        e.preventDefault()
        const stops = [inputRef.current, boardRef.current].filter(Boolean) as HTMLElement[]
        const at = stops.indexOf(document.activeElement as HTMLElement)
        stops[(at + (e.shiftKey ? -1 : 1) + stops.length) % stops.length]?.focus()
        return
      }
    }
  }

  const now = Date.now()
  const row = (r: PaletteRow, i: number) => {
    const s = r.session
    const label = sessionLabel(s)
    const here = s.id === props.current
    const status = s.waiting ? 'waiting' : sessionStatus(s, props.liveBusy?.[s.id], now)
    const at = s.updated ? Date.parse(s.updated) : NaN
    return (
      <div
        key={s.id}
        id={optId(i)}
        role="option"
        aria-selected={i === idx}
        class={`spal-row${i === idx ? ' sel' : ''}${here ? ' here' : ''}`}
        // A row never takes focus, so the click must not move it off the input.
        onMouseDown={(e) => e.preventDefault()}
        onMouseMove={() => i !== idx && setSelId(s.id)}
        onClick={() => pick(s.id)}
      >
        <span class={`spal-dot ${status}`} aria-hidden="true" />
        <span class="spal-text">
          <span class="spal-title">
            {r.match ? (
              <>
                {label.slice(0, r.match[0])}
                <mark>{label.slice(r.match[0], r.match[1])}</mark>
                {label.slice(r.match[1])}
              </>
            ) : (
              label
            )}
          </span>
          <span class="spal-meta">
            {s.model ? s.model + ' · ' : ''}
            {here ? t('this session') : tn(s.messages ?? 0, '%d msg', '%d msgs')}
          </span>
        </span>
        {s.waiting === 'approval' && <span class="spal-chip warn">{t('waits for approval')}</span>}
        {s.waiting === 'question' && <span class="spal-chip warn">{t('waits for an answer')}</span>}
        {!s.waiting && status === 'busy' && <span class="spal-chip busy">{t('working')}</span>}
        {s.origin && <span class="spal-chip">{s.origin}</span>}
        {Number.isFinite(at) && <span class="spal-when">{humanGap(now - at)}</span>}
      </div>
    )
  }

  const keys = paletteShortcutKeys()
  return (
    <div class="modal-scrim spal-scrim" onClick={props.onClose}>
      <div
        class="modal spal"
        role="dialog"
        aria-modal="true"
        aria-label={t('Jump to a session')}
        onClick={(e) => e.stopPropagation()}
        onKeyDown={onKeyDown}
      >
        <div class="spal-q">
          <span class="spal-glass" aria-hidden="true">
            ⌕
          </span>
          <input
            ref={inputRef}
            role="combobox"
            aria-expanded="true"
            aria-controls={`${base}-list`}
            aria-autocomplete="list"
            aria-activedescendant={rows.length ? optId(idx) : undefined}
            aria-label={t('Jump to a session')}
            placeholder={t('Jump to a session…')}
            value={query}
            onKeyDown={onInputKeyDown}
            onInput={(e) => {
              setQuery((e.target as HTMLInputElement).value)
              setSelId('')
            }}
          />
          <span class="spal-count" aria-live="polite">
            {t('%d of %d', list.matched, props.sessions.length)}
          </span>
        </div>
        <div class="spal-list" role="listbox" id={`${base}-list`} aria-label={t('Sessions')}>
          {list.waiting.length > 0 && (
            <div role="group" aria-labelledby={`${base}-gw`}>
              <div class="spal-grp" id={`${base}-gw`} role="presentation">
                {t('Waiting on you')}
              </div>
              {list.waiting.map((r, i) => row(r, i))}
            </div>
          )}
          {list.recent.length > 0 && (
            <div role="group" aria-labelledby={`${base}-gr`}>
              <div class="spal-grp" id={`${base}-gr`} role="presentation">
                {t('Recent')}
              </div>
              {list.recent.map((r, i) => row(r, list.waiting.length + i))}
            </div>
          )}
        </div>
        {rows.length === 0 && (
          <div class="spal-empty">{query.trim() ? t('No session matches “%s”', query.trim()) : t('No sessions yet')}</div>
        )}
        {list.matched > rows.length && (
          <div class="spal-more">{t('Showing %d. Type to narrow the list.', rows.length)}</div>
        )}
        <div class="spal-foot">
          <span aria-hidden="true">
            <kbd>↑</kbd>
            <kbd>↓</kbd> {t('move')}
          </span>
          <span aria-hidden="true">
            {/* i18n-exempt — a key cap, named as the keyboard prints it */}
            <kbd>Enter</kbd> {t('open')}
          </span>
          <span aria-hidden="true">
            {/* i18n-exempt — a key cap, named as the keyboard prints it */}
            <kbd>Esc</kbd> {t('close')}
          </span>
          <span class="spal-shortcut" aria-hidden="true">
            <kbd>{keys[0]}</kbd>
            <kbd>{keys[1]}</kbd>
          </span>
          {props.onOpenBoard && (
            <button ref={boardRef} class="spal-board" onClick={props.onOpenBoard}>
              {t('Open the board')}
            </button>
          )}
        </div>
      </div>
    </div>
  )
}
