// @vitest-environment happy-dom
import { afterEach, describe, expect, it, vi } from 'vitest'
import { cleanup, fireEvent, render, screen } from '@testing-library/preact'
import type { SessionInfo as SessionInfoData } from '../../platform/ctrlproto/types'
import { SessionInfo } from './SessionInfo'
import { SessionPicker } from './SessionPicker'

const session = (overrides: Partial<SessionInfoData> = {}): SessionInfoData => ({
  id: 's1', messages: 2, model: 'model-a', provider: 'provider-a', persona: 'Mieli', path: '/workspace',
  usage: { input: 0, output: 0, cache_read: 0, cache_write: 0, cost_usd: 0.125 }, ...overrides,
})

afterEach(() => { cleanup(); vi.unstubAllGlobals() })

describe('SessionInfo', () => {
  it('renders session details and opens context', () => {
    const onContext = vi.fn()
    render(<SessionInfo info={session()} cost={0.25} onClose={() => {}} onContext={onContext} />)
    expect(screen.getByText('provider-a / model-a')).toBeTruthy()
    expect(screen.getByText('$0.2500')).toBeTruthy()
    fireEvent.click(screen.getByRole('button', { name: 'Context breakdown →' }))
    expect(onContext).toHaveBeenCalledOnce()
  })

  it('copies the workspace path', () => {
    const writeText = vi.fn(async () => {})
    vi.stubGlobal('navigator', { clipboard: { writeText } })
    render(<SessionInfo info={session()} cost={0} onClose={() => {}} onContext={() => {}} />)
    fireEvent.click(screen.getByRole('button', { name: 'copy' }))
    expect(writeText).toHaveBeenCalledWith('/workspace')
  })
})

describe('SessionPicker', () => {
  it('selects sessions and marks the current one active', () => {
    const onSelect = vi.fn()
    render(<SessionPicker sessions={[session(), session({ id: 's2', title: 'Second' })]} current="s2" onSelect={onSelect} onNew={() => {}} onRename={() => {}} onDelete={() => {}} onClose={() => {}} />)
    const current = screen.getByText('Second').closest('.session')
    expect(current?.classList.contains('active')).toBe(true)
    fireEvent.click(screen.getByText('Second'))
    expect(onSelect).toHaveBeenCalledWith('s2')
  })

  it('routes new, rename, and delete without selecting the row', () => {
    const onNew = vi.fn(), onRename = vi.fn(), onDelete = vi.fn(), onSelect = vi.fn()
    const value = session()
    render(<SessionPicker sessions={[value]} current="" onSelect={onSelect} onNew={onNew} onRename={onRename} onDelete={onDelete} onClose={() => {}} />)
    fireEvent.click(screen.getByRole('button', { name: '+ New' }))
    fireEvent.click(screen.getByTitle('Rename'))
    fireEvent.click(screen.getByTitle('Delete'))
    expect(onNew).toHaveBeenCalledOnce()
    expect(onRename).toHaveBeenCalledWith(value)
    expect(onDelete).toHaveBeenCalledWith(value)
    expect(onSelect).not.toHaveBeenCalled()
  })
})
