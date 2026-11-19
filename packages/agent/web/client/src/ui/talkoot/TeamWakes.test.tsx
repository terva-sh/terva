// @vitest-environment happy-dom
import { cleanup, fireEvent, render, screen } from '@testing-library/preact'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { TeamWakes } from './TeamWakes'

afterEach(cleanup)

const due = new Date()
due.setHours(23, 5, 0, 0)

describe('the wakes of a team', () => {
  it('shows nothing for a team with no wake policy and no open wake', () => {
    const { container } = render(<TeamWakes />)
    expect(container.innerHTML).toBe('')
  })

  it('says that wakes are on, with the caps the person set', () => {
    render(<TeamWakes wakes={{ on: true, per_member: 3, max_delay: '24h' }} />)
    expect(screen.getByText('Wakes on: a member may hold 3, each at most 24h ahead')).toBeTruthy()
  })

  it('says why a key that is set turns nothing on', () => {
    render(<TeamWakes wakes={{ on: false, problem: 'talkoot_wakes.per_member is 0, and wakes need 1 or more' }} />)
    expect(screen.getByText('Wakes off')).toBeTruthy()
    expect(screen.getByText('talkoot_wakes.per_member is 0, and wakes need 1 or more')).toBeTruthy()
  })

  it('lists each open wake with its member, its time and its reason, and cancels one', () => {
    const onCancel = vi.fn()
    render(
      <TeamWakes
        wakes={{
          on: true,
          per_member: 2,
          max_delay: '2h',
          open: [
            { id: '01A', member: 'helm', reason: 'Check the CI run.', due: due.toISOString() },
            { id: '01B', member: 'jev', reason: 'Check the deploy.', due: due.toISOString() },
          ],
        }}
        onCancel={onCancel}
      />,
    )
    expect(screen.getByText(/helm: Check the CI run\./)).toBeTruthy()
    expect(screen.getAllByText('23:05').length).toBe(2)
    fireEvent.click(screen.getByLabelText('Cancel the wake of jev'))
    expect(onCancel).toHaveBeenCalledWith('01B')
  })

  it('says that a follow-up belongs to a routine, and cancels it as a wake', () => {
    const onCancel = vi.fn()
    render(
      <TeamWakes
        wakes={{
          on: true,
          per_member: 2,
          max_delay: '2h',
          open: [
            { id: '01A', member: 'helm', reason: 'Check the CI run.', due: due.toISOString() },
            { id: '01C', member: 'helm', reason: 'Look at the deploy again.', due: due.toISOString(), routine: '01R' },
          ],
        }}
        onCancel={onCancel}
      />,
    )
    expect(screen.getByText(/helm, a follow-up of its routine: Look at the deploy again\./)).toBeTruthy()
    // The member's own wake keeps its plain line.
    expect(screen.getByText(/helm: Check the CI run\./)).toBeTruthy()
    fireEvent.click(screen.getAllByLabelText('Cancel the wake of helm')[1])
    expect(onCancel).toHaveBeenCalledWith('01C')
  })

  it('offers no cancel to a viewer that may not steer', () => {
    render(<TeamWakes wakes={{ on: true, per_member: 2, max_delay: '2h', open: [{ id: '01A', member: 'helm', reason: 'Check it.', due: due.toISOString() }] }} />)
    expect(screen.getByText(/helm: Check it\./)).toBeTruthy()
    expect(screen.queryByLabelText('Cancel the wake of helm')).toBeNull()
  })

  it('says that an open wake does not fire while wakes are off', () => {
    render(<TeamWakes wakes={{ on: false, open: [{ id: '01A', member: 'helm', reason: 'Check it.', due: due.toISOString() }] }} />)
    expect(screen.getByText('An open wake does not fire while wakes are off.')).toBeTruthy()
  })
})
