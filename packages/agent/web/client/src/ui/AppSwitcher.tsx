import { useEffect, useRef, useState } from 'preact/hooks'
import { t } from '../i18n'
import type { ServerHello } from '../platform/ctrlproto/types'
import { appsFor, type AppId } from './apps'

let nextId = 0

// The menu is placed with position: fixed from the button's box, rather than
// absolutely inside its parent. The Talkoot rail scrolls (overflow-y: auto),
// and a scroller clips an absolute child on both axes, so a menu wider than
// the rail was cut off. The place travels as custom properties, so the phone
// layout in ui.css can ignore it and draw a bottom sheet instead.
type Place = { top: number; left?: number; right?: number }

// The menu's widest case, for the choice of edge. ui.css caps it at this.
const MENU_WIDTH = 320

/**
 * AppSwitcher names the app on screen and opens a menu of every app the
 * daemon serves. Each entry is a link, so moving between apps is a full
 * navigation, which the apps need: each is its own document and service-worker
 * scope.
 *
 * It renders nothing while there is only one app to list. A daemon that serves
 * the panel alone gets no menu with one entry in it.
 *
 * docs/proposals/web-hub.md, D1, chose this menu over an app rail.
 */
export function AppSwitcher({ hello, current }: { hello: ServerHello | null | undefined; current: AppId }) {
  const [place, setPlace] = useState<Place | null>(null)
  const [menuId] = useState(() => `appsw-${++nextId}`)
  const button = useRef<HTMLButtonElement>(null)
  const menu = useRef<HTMLDivElement>(null)
  const apps = appsFor(hello, current)
  const open = place !== null

  // Focus moves into the menu when it opens, as the WAI menu-button pattern
  // asks, so the arrow keys work at once.
  useEffect(() => {
    if (open) menu.current?.querySelector<HTMLElement>('[role="menuitem"]')?.focus()
  }, [open])

  if (apps.length < 2) return null
  const here = apps.find((a) => a.id === current) ?? apps[0]

  const show = () => {
    const r = button.current?.getBoundingClientRect()
    if (!r) return
    const fitsRight = r.left + MENU_WIDTH <= window.innerWidth - 8
    setPlace(fitsRight ? { top: r.bottom + 6, left: r.left } : { top: r.bottom + 6, right: window.innerWidth - r.right })
  }
  const close = (refocus: boolean) => {
    setPlace(null)
    if (refocus) button.current?.focus()
  }
  const onKeyDown = (e: KeyboardEvent) => {
    if (!open) {
      if (e.key === 'ArrowDown' && e.target === button.current) {
        e.preventDefault()
        show()
      }
      return
    }
    const items = [...(menu.current?.querySelectorAll<HTMLElement>('[role="menuitem"]') ?? [])]
    const at = items.indexOf(document.activeElement as HTMLElement)
    const go = (i: number) => items[(i + items.length) % items.length]?.focus()
    switch (e.key) {
      case 'Escape':
        e.preventDefault()
        close(true)
        break
      case 'ArrowDown':
        e.preventDefault()
        go(at + 1)
        break
      case 'ArrowUp':
        e.preventDefault()
        go(at < 0 ? items.length - 1 : at - 1)
        break
      case 'Home':
        e.preventDefault()
        go(0)
        break
      case 'End':
        e.preventDefault()
        go(items.length - 1)
        break
      case 'Tab':
        close(false)
        break
    }
  }

  const style = place
    ? {
        '--appsw-top': `${place.top}px`,
        '--appsw-left': place.left === undefined ? 'auto' : `${place.left}px`,
        '--appsw-right': place.right === undefined ? 'auto' : `${place.right}px`,
      }
    : undefined

  return (
    <div class={`appsw${open ? ' open' : ''}`} onKeyDown={onKeyDown}>
      <button
        ref={button}
        type="button"
        class="appsw-btn"
        aria-haspopup="menu"
        aria-expanded={open}
        aria-controls={open ? menuId : undefined}
        title={t('Switch app')}
        onClick={() => (open ? close(false) : show())}
      >
        <span class="appsw-mark" aria-hidden="true">
          {here.mark}
        </span>
        <span class="appsw-name">{here.name}</span>
        <span class="appsw-caret" aria-hidden="true">
          ▾
        </span>
      </button>
      {open && (
        <>
          <div class="appsw-scrim" onClick={() => close(false)} />
          <div id={menuId} ref={menu} class="appsw-menu" role="menu" aria-label={t('Apps on this daemon')} style={style}>
            <div class="appsw-head" aria-hidden="true">
              {t('Apps on this daemon')}
            </div>
            {apps.map((a) => (
              <a
                key={a.id}
                role="menuitem"
                class={`appsw-item${a.id === current ? ' cur' : ''}`}
                href={a.href}
                aria-current={a.id === current ? 'page' : undefined}
                // The app on screen is already open. Following its link would
                // reload it and drop whatever the person was doing.
                onClick={
                  a.id === current
                    ? (e: MouseEvent) => {
                        e.preventDefault()
                        close(true)
                      }
                    : undefined
                }
              >
                <span class="appsw-mark" aria-hidden="true">
                  {a.mark}
                </span>
                <span class="appsw-text">
                  <span class="appsw-item-name">{a.name}</span>
                  <span class="appsw-blurb">{a.blurb}</span>
                </span>
                {a.id === current && (
                  <span class="appsw-check" aria-hidden="true">
                    ✓
                  </span>
                )}
              </a>
            ))}
          </div>
        </>
      )}
    </div>
  )
}
