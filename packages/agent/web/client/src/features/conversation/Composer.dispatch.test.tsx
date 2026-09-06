// @vitest-environment happy-dom
import { afterEach, describe, expect, it, vi } from 'vitest'
import { act, cleanup, fireEvent, render, screen } from '@testing-library/preact'
import { Composer } from './Composer'

afterEach(cleanup)
const props = (extra: Partial<Parameters<typeof Composer>[0]> = {}): Parameters<typeof Composer>[0] => ({
  busy: false, onSend: () => true, onToast: () => {}, commands: [], skills: [], onCancel: () => {}, sessionID: 's1', ...extra,
})
const input = () => screen.getByRole('textbox') as HTMLTextAreaElement
const type = (value: string) => fireEvent.input(input(), { target: { value } })

describe('composer dispatch', () => {
  it('keeps input until acceptance and blocks duplicate submissions', async () => {
    let accept!: (v: boolean) => void
    const onSend = vi.fn(() => new Promise<boolean>((r) => { accept = r }))
    render(<Composer {...props({ onSend })} />)
    type('send this')
    fireEvent.keyDown(input(), { key: 'Enter' })
    expect(input().value).toBe('send this')
    fireEvent.keyDown(input(), { key: 'Enter' })
    expect(onSend).toHaveBeenCalledTimes(1)
    await act(async () => accept(true))
    expect(input().value).toBe('')
  })

  it('keeps staged attachments and text on rejection', async () => {
    const onToast = vi.fn()
    const onSend = vi.fn().mockRejectedValue(new Error('dispatch refused'))
    const onUpload = vi.fn().mockResolvedValue({ id: 'att_1', name: 'notes.txt', mime: 'text/plain', kind: 'document', size: 4 })
    const { container } = render(<Composer {...props({ onSend, onToast, onUpload, canAttachFiles: true })} />)
    await act(async () => { fireEvent.drop(container.querySelector('footer')!, { dataTransfer: { files: [new File(['text'], 'notes.txt', { type: 'text/plain' })] } }) })
    type('my notes')
    await act(async () => { fireEvent.keyDown(input(), { key: 'Enter' }) })
    expect(input().value).toBe('my notes')
    expect(screen.getByText('notes.txt')).toBeTruthy()
    expect(onSend.mock.calls[0][2][0].id).toBe('att_1')
    expect(onToast).toHaveBeenCalled()
  })

  it('does not erase newer text or its persisted draft on acceptance', async () => {
    let accept!: (v: boolean) => void
    const onSaveDraft = vi.fn().mockResolvedValue(undefined)
    render(<Composer {...props({ onSaveDraft, onSend: () => new Promise<boolean>((r) => { accept = r }) })} />)
    type('first')
    fireEvent.keyDown(input(), { key: 'Enter' })
    type('new writing')
    await act(async () => accept(true))
    expect(input().value).toBe('new writing')
    fireEvent(window, new Event('pagehide'))
    expect(onSaveDraft).toHaveBeenLastCalledWith('s1', 'new writing')
  })

  it('does not clear another session when the old send is accepted', async () => {
    let accept!: (v: boolean) => void
    const p = props({ onSend: () => new Promise<boolean>((r) => { accept = r }) })
    const { rerender } = render(<Composer {...p} />)
    type('first')
    fireEvent.keyDown(input(), { key: 'Enter' })
    rerender(<Composer {...p} sessionID="s2" />)
    type('second session')
    await act(async () => accept(true))
    expect(input().value).toBe('second session')
  })

  for (const key of ['Enter', 'Tab', 'ArrowDown', 'Escape']) {
    it(`leaves ${key} to an active IME, including completion menus`, () => {
      const onSend = vi.fn(() => true)
      render(<Composer {...props({ onSend, commands: [{ name: 'help', desc: 'help', run: () => {} }] })} />)
      type('/he')
      fireEvent.compositionStart(input())
      expect(fireEvent.keyDown(input(), { key, isComposing: true })).toBe(true)
      expect(input().value).toBe('/he')
      expect(onSend).not.toHaveBeenCalled()
      fireEvent.compositionEnd(input())
    })
  }
})
