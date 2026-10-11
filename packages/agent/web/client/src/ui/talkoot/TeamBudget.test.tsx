// @vitest-environment happy-dom
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/preact'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { fakeClient } from '../../platform/ctrlproto/testing'
import type { TalkootView } from '../../platform/ctrlproto/types'
import { TeamBudget } from './TeamBudget'
import { RoomLine } from './RoomLine'

const view: TalkootView = { id: 'crew', name: 'Crew', home: '/repo', budget_usd_per_day: 20, members: [] }
afterEach(cleanup)

describe('team daily limit', () => {
  it('shows the cap and sends only the persistent waiver', async () => {
    const client = fakeClient()
    render(<TeamBudget client={client} view={view} person="Drew" canSteer onError={vi.fn()} />)
    expect(screen.getByText('Team daily limit: $20.00')).toBeTruthy()
    fireEvent.click(screen.getByRole('button', { name: 'Waive team daily limit' }))
    await waitFor(() => expect(client.last('talkoot.update')?.params).toEqual({ id: 'crew', by: 'Drew', team_budget_waived: true }))
  })

  it('shows a persisted waiver and restores the configured cap', async () => {
    const client = fakeClient()
    render(<TeamBudget client={client} view={{ ...view, team_budget_waived: true }} person="Drew" canSteer onError={vi.fn()} />)
    expect(screen.getByText('Team daily limit waived')).toBeTruthy()
    expect(screen.getByText(/Member limits still apply/)).toBeTruthy()
    fireEvent.click(screen.getByRole('button', { name: 'Restore team daily limit' }))
    await waitFor(() => expect(client.last('talkoot.update')?.params).toEqual({ id: 'crew', by: 'Drew', team_budget_waived: false }))
  })

  it('retains the displayed cap on refusal and prevents duplicate requests while saving', async () => {
    let refuse!: (error: Error) => void
    const client = fakeClient({ respond: () => new Promise((_, reject) => { refuse = reject }) })
    const onError = vi.fn()
    render(<TeamBudget client={client} view={view} person="Drew" canSteer onError={onError} />)
    const button = screen.getByRole('button', { name: 'Waive team daily limit' }) as HTMLButtonElement
    fireEvent.click(button)
    await waitFor(() => expect(button.disabled).toBe(true))
    fireEvent.click(button)
    expect(client.sent('talkoot.update')).toHaveLength(1)
    refuse(new Error('steer denied'))
    await waitFor(() => expect(onError).toHaveBeenCalledWith('steer denied'))
    await waitFor(() => expect(button.disabled).toBe(false))
    expect(screen.getByText('Team daily limit: $20.00')).toBeTruthy()
  })

  it('lets a spectator see the cap but offers no toggle', () => {
    render(<TeamBudget client={fakeClient()} view={view} person="" canSteer={false} onError={vi.fn()} />)
    expect(screen.getByText('Team daily limit: $20.00')).toBeTruthy()
    expect(screen.queryByRole('button')).toBeNull()
  })

  it('shows the attributed waiver in the room', () => {
    render(<RoomLine line={{ type: 'roster', at: '2026-09-30T20:00:00Z', by: 'human:Drew', reason: 'the team daily cost limit is waived; member limits still apply' }} />)
    expect(screen.getByText(/Drew changed the roster; the team daily cost limit is waived/)).toBeTruthy()
  })
})
