// @vitest-environment happy-dom
import { afterEach, describe, expect, it, vi } from 'vitest'
import { cleanup, fireEvent, render, screen } from '@testing-library/preact'
import type { SessionInfo } from '../../platform/ctrlproto/types'
import { anotherDialogOpen, isPaletteShortcut, SessionPalette } from './SessionPalette'

afterEach(cleanup)

const s = (id: string, title: string, more: Partial<SessionInfo> = {}): SessionInfo =>
  ({ id, title, messages: 3, ...more }) as SessionInfo

const sessions = [
  s('here', 'Refactor the settings surface'),
  s('flaky', 'Fix the flaky procgroup test', { waiting: 'approval', live: true, busy: true }),
  s('kick', 'Talkoot kickoff race', { live: true }),
  s('neot/20261011-090000-1a2b3c4d', 'Disk usage report', { origin: 'neot' }),
]

function open(props: Partial<Parameters<typeof SessionPalette>[0]> = {}) {
  const onSelect = vi.fn()
  const onClose = vi.fn()
  render(<SessionPalette sessions={sessions} current="here" onSelect={onSelect} onClose={onClose} {...props} />)
  const input = screen.getByRole('combobox')
  return { onSelect, onClose, input }
}

const options = () => screen.getAllByRole('option')
const selected = () => options().find((o) => o.getAttribute('aria-selected') === 'true')

describe('SessionPalette', () => {
  it('is a modal dialog around a combobox that drives a listbox', () => {
    const { input } = open()
    const dialog = screen.getByRole('dialog')
    expect(dialog.getAttribute('aria-modal')).toBe('true')
    const listbox = screen.getByRole('listbox')
    expect(input.getAttribute('aria-controls')).toBe(listbox.id)
    expect(document.activeElement).toBe(input)
    // The input points at the selected row; the row never takes focus.
    expect(input.getAttribute('aria-activedescendant')).toBe(selected()!.id)
  })

  it('lists the waiting first, then the rest newest first, and starts on another session', () => {
    open()
    expect(screen.getAllByRole('group').map((g) => g.textContent?.slice(0, 14))).toEqual([
      'Waiting on you',
      'RecentRefactor',
    ])
    expect(options().map((o) => o.querySelector('.spal-title')?.textContent)).toEqual([
      'Fix the flaky procgroup test',
      'Refactor the settings surface',
      'Talkoot kickoff race',
      'Disk usage report',
    ])
    // The default is the first row that is not the session on screen.
    expect(selected()!.textContent).toContain('Fix the flaky')
  })

  it('marks what a row waits on, a row at work, and a fleet member', () => {
    open()
    const [flaky, here, , disk] = options()
    expect(flaky.textContent).toContain('waits for approval')
    // Waiting outranks busy: the person is who can move it.
    expect(flaky.textContent).not.toContain('working')
    expect(flaky.querySelector('.spal-dot')?.classList.contains('waiting')).toBe(true)
    expect(here.textContent).toContain('this session')
    expect(disk.querySelector('.spal-chip')?.textContent).toBe('neot')
  })

  it('reads busy from a live subscription over the list', () => {
    open({ liveBusy: { kick: true } })
    expect(options()[2].textContent).toContain('working')
  })

  it('filters by title as the person types, and marks the match', () => {
    const { input } = open()
    fireEvent.input(input, { target: { value: 'kick' } })
    expect(options()).toHaveLength(1)
    expect(options()[0].querySelector('mark')?.textContent).toBe('kick')
    expect(screen.getByText('1 of 4')).toBeTruthy()
    fireEvent.input(input, { target: { value: 'nothing like it' } })
    expect(screen.queryAllByRole('option')).toHaveLength(0)
    expect(screen.getByText('No session matches “nothing like it”')).toBeTruthy()
  })

  it('moves with the arrow keys, wrapping, and opens the selection on Enter', () => {
    const { input, onSelect } = open()
    fireEvent.keyDown(input, { key: 'ArrowDown' })
    expect(selected()!.textContent).toContain('Refactor')
    fireEvent.keyDown(input, { key: 'ArrowUp' })
    fireEvent.keyDown(input, { key: 'ArrowUp' })
    expect(selected()!.textContent).toContain('Disk usage')
    expect(input.getAttribute('aria-activedescendant')).toBe(selected()!.id)
    fireEvent.keyDown(input, { key: 'Enter' })
    // A federated id travels whole.
    expect(onSelect).toHaveBeenCalledWith('neot/20261011-090000-1a2b3c4d')
  })

  it('leaves Enter and the arrows to an IME while it composes', () => {
    const { input, onSelect } = open()
    const first = selected()!.id
    fireEvent.compositionStart(input)
    fireEvent.keyDown(input, { key: 'ArrowDown' })
    fireEvent.keyDown(input, { key: 'Enter' })
    expect(selected()!.id).toBe(first)
    expect(onSelect).not.toHaveBeenCalled()
    // Some IMEs end the composition before the confirming keydown, which then
    // arrives as keyCode 229.
    fireEvent.compositionEnd(input)
    fireEvent.keyDown(input, { key: 'Enter', keyCode: 229 })
    expect(onSelect).not.toHaveBeenCalled()
    // The positive control: once the IME is done, Enter opens the selection.
    fireEvent.keyDown(input, { key: 'Enter' })
    expect(onSelect).toHaveBeenCalledWith('flaky')
  })

  it('closes, rather than reopening, when the session on screen is picked', () => {
    const { onSelect, onClose } = open()
    fireEvent.click(options()[1])
    expect(onSelect).not.toHaveBeenCalled()
    expect(onClose).toHaveBeenCalled()
  })

  it('closes on Escape without letting the key reach the panel', () => {
    const { input, onClose } = open()
    const outer = vi.fn()
    window.addEventListener('keydown', outer)
    // The positive control: a key the palette does not stop reaches window,
    // so silence below means the stop and not a listener that never heard.
    fireEvent.keyDown(input, { key: 'ArrowDown' })
    expect(outer).toHaveBeenCalledTimes(1)
    fireEvent.keyDown(input, { key: 'Escape' })
    window.removeEventListener('keydown', outer)
    expect(onClose).toHaveBeenCalled()
    expect(outer).toHaveBeenCalledTimes(1)
  })

  it('keeps Tab inside the dialog', () => {
    const { input } = open({ onOpenBoard: () => {} })
    const board = screen.getByRole('button', { name: 'Open the board' })
    fireEvent.keyDown(input, { key: 'Tab' })
    expect(document.activeElement).toBe(board)
    fireEvent.keyDown(board, { key: 'Tab' })
    expect(document.activeElement).toBe(input)
    fireEvent.keyDown(input, { key: 'Tab', shiftKey: true })
    expect(document.activeElement).toBe(board)
  })

  it('leaves Enter on the board button to the button', () => {
    const { input, onSelect } = open({ onOpenBoard: () => {} })
    const board = screen.getByRole('button', { name: 'Open the board' })
    fireEvent.keyDown(input, { key: 'Tab' })
    expect(document.activeElement).toBe(board)
    // Not prevented, so the browser activates the button; and no session opens.
    expect(fireEvent.keyDown(board, { key: 'Enter' })).toBe(true)
    expect(onSelect).not.toHaveBeenCalled()
    // The positive control: Enter in the input does open the selection.
    fireEvent.keyDown(input, { key: 'Enter' })
    expect(onSelect).toHaveBeenCalledWith('flaky')
  })

  it('closes on a click outside', () => {
    const { onClose } = open()
    fireEvent.click(document.querySelector('.modal-scrim')!)
    expect(onClose).toHaveBeenCalled()
  })
})

describe('anotherDialogOpen', () => {
  it('sees another modal and not the palette itself', () => {
    expect(anotherDialogOpen()).toBe(false)
    const { input } = open()
    expect(input).toBeTruthy()
    // The palette's own scrim does not count, so Mod+K can close it.
    expect(anotherDialogOpen()).toBe(false)
    cleanup()
    for (const html of ['<div class="modal-scrim"></div>', '<div class="landing-scrim"></div>', '<div role="dialog" aria-modal="true"></div>']) {
      const host = document.createElement('div')
      host.innerHTML = html
      document.body.appendChild(host)
      expect(anotherDialogOpen(), html).toBe(true)
      host.remove()
    }
  })
})

describe('isPaletteShortcut', () => {
  const key = (init: KeyboardEventInit) => new KeyboardEvent('keydown', init)

  it('is Cmd+K on a Mac and Ctrl+K elsewhere', () => {
    expect(isPaletteShortcut(key({ key: 'k', metaKey: true }), true)).toBe(true)
    expect(isPaletteShortcut(key({ key: 'k', ctrlKey: true }), false)).toBe(true)
    // Ctrl+K in a Mac text field deletes to the end of the line (D7).
    expect(isPaletteShortcut(key({ key: 'k', ctrlKey: true }), true)).toBe(false)
    expect(isPaletteShortcut(key({ key: 'k', metaKey: true }), false)).toBe(false)
    // Caps Lock sends an upper-case K.
    expect(isPaletteShortcut(key({ key: 'K', ctrlKey: true }), false)).toBe(true)
  })

  it('leaves the key alone with another modifier or without one', () => {
    expect(isPaletteShortcut(key({ key: 'k' }), false)).toBe(false)
    expect(isPaletteShortcut(key({ key: 'k', ctrlKey: true, shiftKey: true }), false)).toBe(false)
    expect(isPaletteShortcut(key({ key: 'k', ctrlKey: true, altKey: true }), false)).toBe(false)
    expect(isPaletteShortcut(key({ key: 'j', ctrlKey: true }), false)).toBe(false)
  })
})
