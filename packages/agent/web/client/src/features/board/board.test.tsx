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

// Which group a tile ended up in. Tiles carry no status pill of their own —
// the header says it once — so the grouping IS the assertion.
const groupOf = (title: string) =>
  [...(screen.getByText(title).closest('.session-section')?.classList ?? [])]
    .find((c) => c.startsWith('session-section--'))
    ?.slice('session-section--'.length)

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

  it('files a tile under busy over live over cold', () => {
    // Each state gets its OWN mount rather than a rerender. Whether the cold
    // group is open is decided once at mount, so a board that opened on a busy
    // session keeps cold collapsed when that session later goes cold — correct
    // behaviour, and it would hide the tile this asserts.
    const groupFor = (s: Partial<SessionInfoData>) => {
      cleanup()
      render(
        <SessionsBoard sessions={[session({ ...s, title: 'T' })]} current="" onSelect={noop} onNew={noop} onRename={noop} onDelete={noop} />,
      )
      return groupOf('T')
    }
    expect(groupFor({ busy: true, live: true })).toBe('busy')
    expect(groupFor({ busy: false, live: true })).toBe('idle')
    expect(groupFor({})).toBe('cold')
  })

  it('prefers streamed liveBusy over the list point-in-time flag', () => {
    // The list row says idle (busy:false, live:true) but a live subscription
    // reports a turn in flight — the tile belongs under busy.
    render(
      <SessionsBoard sessions={[session({ id: 's1', title: 'T', busy: false, live: true })]} current="" liveBusy={{ s1: true }} onSelect={noop} onNew={noop} onRename={noop} onDelete={noop} />,
    )
    expect(groupOf('T')).toBe('busy')
    // A streamed entry makes the tile live+idle even if the list hadn't marked it.
    cleanup()
    render(<SessionsBoard sessions={[session({ id: 's1', title: 'T' })]} current="" liveBusy={{ s1: false }} onSelect={noop} onNew={noop} onRename={noop} onDelete={noop} />)
    expect(groupOf('T')).toBe('idle')
  })

  // The pill is gone on purpose: the group header says the state once, and
  // repeating it on every tile made the loudest thing on the board the label
  // rather than the session that was moving.
  it('carries no per-tile status pill', () => {
    const { container } = render(
      <SessionsBoard sessions={[session({ title: 'T', busy: true, live: true })]} current="" onSelect={noop} onNew={noop} onRename={noop} onDelete={noop} />,
    )
    expect(container.querySelector('.board-tile .board-status')).toBeNull()
    expect(groupOf('T')).toBe('busy')
  })

  // The grid is grouped now: a tile lands under the header for its state, and
  // the cold group — the one that grows without bound — starts collapsed as
  // long as there is anything else to show.
  it('groups tiles by state and collapses cold behind a disclosure', () => {
    render(
      <SessionsBoard
        sessions={[session({ id: 'w', title: 'Working', busy: true, live: true }), session({ id: 'c', title: 'Old' })]}
        current="" onSelect={noop} onNew={noop} onRename={noop} onDelete={noop}
      />,
    )
    expect(screen.getByText('Working').closest('.session-section')?.classList.contains('session-section--busy')).toBe(true)
    expect(screen.queryByText('Old')).toBeNull()

    const toggle = screen.getByRole('button', { expanded: false })
    fireEvent.click(toggle)
    expect(screen.getByText('Old').closest('.session-section')?.classList.contains('session-section--cold')).toBe(true)
  })

  // ⚠️ The seed must come from the first sectioning that HAS a list, not from
  // the first render. sessions.list is a round trip after the hello, so the
  // board's first render is against an UNANSWERED list — and "cold is all there
  // is" is vacuously true of nothing. Seeded there, the cold group opened on
  // every load and the collapse shipped doing nothing. A browser smoke caught
  // this; happy-dom would not have, because nothing here mounts before its data
  // unless the test makes it.
  it('does not seed the cold group off a list that has not answered yet', () => {
    const { rerender } = render(
      <SessionsBoard sessions={[]} loaded={false} current="" onSelect={noop} onNew={noop} onRename={noop} onDelete={noop} />,
    )
    rerender(
      <SessionsBoard
        sessions={[session({ id: 'w', title: 'Working', busy: true, live: true }), session({ id: 'c', title: 'Old' })]}
        loaded current="" onSelect={noop} onNew={noop} onRename={noop} onDelete={noop}
      />,
    )
    expect(screen.getByText('Working')).toBeTruthy()
    expect(screen.queryByText('Old'), 'cold must be collapsed once the real list arrives').toBeNull()
  })

  // An expanded cold group must survive the 4s re-list. The board stays mounted
  // while you are on it, so deriving the open state from props every render
  // would snap the group shut under the reader the moment anything went busy.
  it('keeps cold open across a re-render once the user has opened it', () => {
    const { rerender } = render(
      <SessionsBoard sessions={[session({ id: 'c', title: 'Old' })]} current="" onSelect={noop} onNew={noop} onRename={noop} onDelete={noop} />,
    )
    // Nothing busy or idle, so cold IS the list and opens on its own.
    expect(screen.getByText('Old')).toBeTruthy()
    rerender(
      <SessionsBoard
        sessions={[session({ id: 'c', title: 'Old' }), session({ id: 'w', title: 'Working', busy: true, live: true })]}
        current="" onSelect={noop} onNew={noop} onRename={noop} onDelete={noop}
      />,
    )
    expect(screen.getByText('Old')).toBeTruthy()
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
    render(<SessionsBoard sessions={[]} loaded current="" onSelect={noop} onNew={noop} onRename={noop} onDelete={noop} />)
    expect(screen.getByText('No sessions in this workspace yet.')).toBeTruthy()
  })

  // An empty list means two different things, and before `loaded` existed the
  // board could not tell them apart: it rendered "No sessions in this workspace
  // yet." off app.tsx's useState default, so a panel that had merely finished
  // painting told you your workspace was empty while the socket was still
  // connecting. sessions.list is a round trip AFTER the hello, so `open` is not
  // enough either — only the answer settles it.
  it('does not claim an empty workspace before sessions.list has answered', () => {
    const { rerender } = render(
      <SessionsBoard sessions={[]} loaded={false} current="" onSelect={noop} onNew={noop} onRename={noop} onDelete={noop} />,
    )
    expect(screen.queryByText('No sessions in this workspace yet.')).toBeNull()
    expect(screen.getByText('Loading sessions…')).toBeTruthy()

    // …and the moment it answers "none", the empty state is the honest thing.
    rerender(<SessionsBoard sessions={[]} loaded current="" onSelect={noop} onNew={noop} onRename={noop} onDelete={noop} />)
    expect(screen.queryByText('Loading sessions…')).toBeNull()
    expect(screen.getByText('No sessions in this workspace yet.')).toBeTruthy()
  })

  // Optional prop: a caller that has no such flag (an embedding, an older test)
  // must keep the previous behaviour rather than be stuck on a placeholder that
  // nothing will ever clear.
  it('keeps the plain empty state for a caller that passes no loaded flag', () => {
    render(<SessionsBoard sessions={[]} current="" onSelect={noop} onNew={noop} onRename={noop} onDelete={noop} />)
    expect(screen.getByText('No sessions in this workspace yet.')).toBeTruthy()
  })
})
