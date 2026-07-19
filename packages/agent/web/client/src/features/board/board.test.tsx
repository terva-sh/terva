// @vitest-environment happy-dom
import { afterEach, describe, expect, it, vi } from 'vitest'
import { cleanup, fireEvent, render, screen } from '@testing-library/preact'
import type { SessionInfo as SessionInfoData } from '../../platform/ctrlproto/types'
import { SessionsBoard } from './SessionsBoard'

const session = (overrides: Partial<SessionInfoData> = {}): SessionInfoData => ({
  id: 's1', messages: 2, model: 'model-a', provider: 'provider-a', path: '/w',
  usage: { input: 0, output: 0, cache_read: 0, cache_write: 0, cost_usd: 0.125 }, ...overrides,
})

const noop = () => {}

afterEach(() => { cleanup(); vi.unstubAllGlobals() })

describe('SessionsBoard', () => {
  it('renders a tile per session, marks the current one, and focuses on click', () => {
    const onSelect = vi.fn()
    render(
      <SessionsBoard
        sessions={[session({ id: 's1', title: 'First' }), session({ id: 's2', title: 'Second' })]}
        current="s2" onSelect={onSelect} onNew={noop} onRename={noop} onDelete={noop}
      />,
    )
    const active = screen.getByText('Second').closest('.board-tile')
    expect(active?.classList.contains('active')).toBe(true)
    fireEvent.click(screen.getByText('First'))
    expect(onSelect).toHaveBeenCalledWith('s1')
  })

  it('shows busy over live over cold as the tile status', () => {
    const { rerender } = render(
      <SessionsBoard sessions={[session({ busy: true, live: true })]} current="" onSelect={noop} onNew={noop} onRename={noop} onDelete={noop} />,
    )
    expect(screen.getByText('busy')).toBeTruthy()
    rerender(<SessionsBoard sessions={[session({ busy: false, live: true })]} current="" onSelect={noop} onNew={noop} onRename={noop} onDelete={noop} />)
    expect(screen.getByText('idle')).toBeTruthy()
    rerender(<SessionsBoard sessions={[session({})]} current="" onSelect={noop} onNew={noop} onRename={noop} onDelete={noop} />)
    expect(screen.getByText('cold')).toBeTruthy()
  })

  it('prefers streamed liveBusy over the list point-in-time flag', () => {
    // The list row says idle (busy:false, live:true) but a live subscription
    // reports a turn in flight — the tile must show busy.
    const { rerender } = render(
      <SessionsBoard sessions={[session({ id: 's1', busy: false, live: true })]} current="" liveBusy={{ s1: true }} onSelect={noop} onNew={noop} onRename={noop} onDelete={noop} />,
    )
    expect(screen.getByText('busy')).toBeTruthy()
    // A streamed entry makes the tile live+idle even if the list hadn't marked it.
    rerender(<SessionsBoard sessions={[session({ id: 's1' })]} current="" liveBusy={{ s1: false }} onSelect={noop} onNew={noop} onRename={noop} onDelete={noop} />)
    expect(screen.getByText('idle')).toBeTruthy()
  })

  it('routes new, rename, and delete without selecting the tile', () => {
    const onNew = vi.fn(), onRename = vi.fn(), onDelete = vi.fn(), onSelect = vi.fn()
    const value = session()
    render(<SessionsBoard sessions={[value]} current="" onSelect={onSelect} onNew={onNew} onRename={onRename} onDelete={onDelete} />)
    fireEvent.click(screen.getByRole('button', { name: '+ New' }))
    fireEvent.click(screen.getByTitle('Rename'))
    fireEvent.click(screen.getByTitle('Delete'))
    expect(onNew).toHaveBeenCalledOnce()
    expect(onRename).toHaveBeenCalledWith(value)
    expect(onDelete).toHaveBeenCalledWith(value)
    expect(onSelect).not.toHaveBeenCalled()
  })

  it('renders an empty state when there are no sessions', () => {
    render(<SessionsBoard sessions={[]} current="" onSelect={noop} onNew={noop} onRename={noop} onDelete={noop} />)
    expect(screen.getByText('No sessions in this workspace yet.')).toBeTruthy()
  })
})
