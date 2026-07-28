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
    render(<SessionPicker sessions={[session(), session({ id: 's2', title: 'Second' })]} current="s2" onSelect={onSelect} onNew={() => {}} onRename={() => {}} onGenerateTitle={() => {}} onDelete={() => {}} onClose={() => {}} />)
    const current = screen.getByText('Second').closest('.session')
    expect(current?.classList.contains('active')).toBe(true)
    fireEvent.click(screen.getByText('Second'))
    expect(onSelect).toHaveBeenCalledWith('s2')
  })

  it('routes new, rename, generate title, and delete without selecting the row', () => {
    const onNew = vi.fn(), onRename = vi.fn(), onGenerateTitle = vi.fn(), onDelete = vi.fn(), onSelect = vi.fn()
    const value = session()
    render(<SessionPicker sessions={[value]} current="" onSelect={onSelect} onNew={onNew} onRename={onRename} onGenerateTitle={onGenerateTitle} onDelete={onDelete} onClose={() => {}} />)
    fireEvent.click(screen.getByRole('button', { name: '+ New' }))
    fireEvent.click(screen.getByTitle('Rename'))
    fireEvent.click(screen.getByTitle('Generate title'))
    fireEvent.click(screen.getByTitle('Delete'))
    expect(onNew).toHaveBeenCalledOnce()
    expect(onRename).toHaveBeenCalledWith(value)
    expect(onGenerateTitle).toHaveBeenCalledWith(value)
    expect(onDelete).toHaveBeenCalledWith(value)
    expect(onSelect).not.toHaveBeenCalled()
  })

  // The drawer overloads fast: every 2-message probe you ever ran sits between
  // you and the session you are actually in. Grouping by state and collapsing
  // cold is what puts the working set back at the top.
  it('groups rows by state and collapses cold behind a disclosure', () => {
    render(
      <SessionPicker
        sessions={[
          session({ id: 'w', title: 'Working', live: true, busy: true }),
          session({ id: 'h', title: 'Held', live: true }),
          session({ id: 'c', title: 'Old' }),
        ]}
        current="" liveBusy={{}} onSelect={() => {}} onNew={() => {}} onRename={() => {}}
        onGenerateTitle={() => {}} onDelete={() => {}} onClose={() => {}}
      />,
    )
    expect(screen.getByText('Working').closest('.session-section')?.classList.contains('session-section--busy')).toBe(true)
    expect(screen.getByText('Held').closest('.session-section')?.classList.contains('session-section--idle')).toBe(true)
    expect(screen.queryByText('Old')).toBeNull()

    fireEvent.click(screen.getByRole('button', { expanded: false }))
    expect(screen.getByText('Old').closest('.session-section')?.classList.contains('session-section--cold')).toBe(true)
  })

  // The whole point is to hide the tail — but a drawer whose every group is
  // empty except one collapsed disclosure has hidden everything it had. That is
  // the ORDINARY case in focus mode against a freshly restarted daemon.
  it('opens cold when there is nothing else to show', () => {
    render(
      <SessionPicker
        sessions={[session({ id: 'a', title: 'Only' })]}
        current="" onSelect={() => {}} onNew={() => {}} onRename={() => {}}
        onGenerateTitle={() => {}} onDelete={() => {}} onClose={() => {}}
      />,
    )
    expect(screen.getByText('Only')).toBeTruthy()
  })

  // A session the daemon has forgotten (restart, eviction) is not "old" — the
  // transcript's mtime says you were in it minutes ago, and collapsing it away
  // is exactly wrong at exactly the wrong moment.
  it('keeps a recently-touched session out of the collapsed group', () => {
    render(
      <SessionPicker
        sessions={[
          session({ id: 'h', title: 'Held', live: true }),
          session({ id: 'r', title: 'Recent', updated: new Date(Date.now() - 60_000).toISOString() }),
          session({ id: 'o', title: 'Ancient', updated: '2020-01-01T00:00:00Z' }),
        ]}
        current="" onSelect={() => {}} onNew={() => {}} onRename={() => {}}
        onGenerateTitle={() => {}} onDelete={() => {}} onClose={() => {}}
      />,
    )
    expect(screen.getByText('Recent').closest('.session-section')?.classList.contains('session-section--idle')).toBe(true)
    expect(screen.queryByText('Ancient')).toBeNull()
  })

  // The focused session must never be the thing that got hidden. app.tsx always
  // seeds liveBusy[current], so it is subscribed by definition — assert the end
  // result rather than trusting that invariant to stay true.
  it('never hides the focused session inside the collapsed group', () => {
    render(
      <SessionPicker
        sessions={[session({ id: 'cur', title: 'Focused' }), session({ id: 'c', title: 'Old' })]}
        current="cur" liveBusy={{ cur: false }} onSelect={() => {}} onNew={() => {}} onRename={() => {}}
        onGenerateTitle={() => {}} onDelete={() => {}} onClose={() => {}}
      />,
    )
    const row = screen.getByText('Focused').closest('.session')
    expect(row?.classList.contains('active')).toBe(true)
    expect(screen.queryByText('Old')).toBeNull()
  })
})
