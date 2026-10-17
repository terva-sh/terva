// @vitest-environment happy-dom
import { afterEach, describe, expect, it } from 'vitest'
import { cleanup, fireEvent, render, screen } from '@testing-library/preact'
import type { ServerHello } from '../platform/ctrlproto/types'
import { AppSwitcher } from './AppSwitcher'

afterEach(cleanup)

const both: ServerHello = { role: 'server', groups: ['talkoot'], features: ['stage'] }

function openMenu() {
  fireEvent.click(screen.getByRole('button', { name: /Panel/ }))
  return screen.getByRole('menu')
}

describe('AppSwitcher', () => {
  it('renders nothing while the panel is the only app', () => {
    // A menu with one entry is noise on every daemon that serves only the panel.
    const { container } = render(<AppSwitcher hello={{ role: 'server' }} current="panel" />)
    expect(container.innerHTML).toBe('')
  })

  it('names the app on screen and lists every app as a link', () => {
    render(<AppSwitcher hello={both} current="panel" />)
    const button = screen.getByRole('button', { name: /Panel/ })
    expect(button.getAttribute('aria-haspopup')).toBe('menu')
    expect(button.getAttribute('aria-expanded')).toBe('false')

    const menu = openMenu()
    expect(button.getAttribute('aria-expanded')).toBe('true')
    expect(button.getAttribute('aria-controls')).toBe(menu.id)
    const items = screen.getAllByRole('menuitem')
    expect(items.map((a) => a.getAttribute('href'))).toEqual(['/', '/talkoot/', '/stage/'])
    // The app on screen is marked as the current page, and only that one.
    expect(items.map((a) => a.getAttribute('aria-current'))).toEqual(['page', null, null])
  })

  it('moves focus into the menu and through it with the arrow keys', () => {
    render(<AppSwitcher hello={both} current="panel" />)
    openMenu()
    const items = screen.getAllByRole('menuitem')
    expect(document.activeElement).toBe(items[0])
    fireEvent.keyDown(items[0], { key: 'ArrowDown' })
    expect(document.activeElement).toBe(items[1])
    fireEvent.keyDown(items[1], { key: 'End' })
    expect(document.activeElement).toBe(items[2])
    // Down from the last wraps to the first, as a menu does.
    fireEvent.keyDown(items[2], { key: 'ArrowDown' })
    expect(document.activeElement).toBe(items[0])
    fireEvent.keyDown(items[0], { key: 'ArrowUp' })
    expect(document.activeElement).toBe(items[2])
  })

  it('opens from the keyboard with ArrowDown on the button', () => {
    render(<AppSwitcher hello={both} current="panel" />)
    const button = screen.getByRole('button', { name: /Panel/ })
    fireEvent.keyDown(button, { key: 'ArrowDown' })
    expect(screen.getByRole('menu')).toBeTruthy()
  })

  it('closes on Escape and gives focus back to the button', () => {
    render(<AppSwitcher hello={both} current="panel" />)
    openMenu()
    fireEvent.keyDown(screen.getAllByRole('menuitem')[1], { key: 'Escape' })
    expect(screen.queryByRole('menu')).toBeNull()
    expect(document.activeElement).toBe(screen.getByRole('button', { name: /Panel/ }))
  })

  it('closes on a click outside the menu', () => {
    const { container } = render(<AppSwitcher hello={both} current="panel" />)
    openMenu()
    fireEvent.click(container.querySelector('.appsw-scrim')!)
    expect(screen.queryByRole('menu')).toBeNull()
  })

  it('does not reload the app on screen when its own entry is picked', () => {
    render(<AppSwitcher hello={both} current="panel" />)
    openMenu()
    const here = screen.getAllByRole('menuitem')[0]
    const followed = fireEvent.click(here)
    // fireEvent returns false when a handler called preventDefault.
    expect(followed).toBe(false)
    expect(screen.queryByRole('menu')).toBeNull()
  })

  it('lets a link to another app navigate', () => {
    render(<AppSwitcher hello={both} current="panel" />)
    openMenu()
    expect(fireEvent.click(screen.getAllByRole('menuitem')[1])).toBe(true)
  })
})
