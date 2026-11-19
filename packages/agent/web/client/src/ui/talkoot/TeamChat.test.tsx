// @vitest-environment happy-dom
import { cleanup, render, screen } from '@testing-library/preact'
import { afterEach, describe, expect, it } from 'vitest'
import { TeamChat } from './TeamChat'

afterEach(cleanup)

describe('the chat bridge of a team', () => {
  it('shows nothing for a team with no bridge', () => {
    const { container } = render(<TeamChat />)
    expect(container.innerHTML).toBe('')
  })

  it('names the connector and the account while the bridge is on', () => {
    render(<TeamChat chat={{ state: 'connected', connector: 'matrix', username: 'mieli', paired_id: '@sothr:example.org', talkoot: 'crew' }} />)
    expect(screen.getByText('Chat bridge on: matrix as @mieli')).toBeTruthy()
    expect(screen.getByText(/enter this room as yours/)).toBeTruthy()
  })

  it('shows the error of a bridge that is down, such as one with no paired account', () => {
    render(<TeamChat chat={{ state: 'error', connector: 'matrix', error: 'no account is paired with this connector', talkoot: 'crew' }} />)
    expect(screen.getByText('Chat bridge down: matrix')).toBeTruthy()
    expect(screen.getByText('no account is paired with this connector')).toBeTruthy()
    expect(screen.queryByText(/enter this room as yours/)).toBeNull()
  })

  it('lists what the bridge holds back, with why and when each goes out', () => {
    const at = new Date(2026, 10, 17, 23, 5).toISOString()
    const ends = new Date(2026, 10, 18, 7, 0).toISOString()
    const brief = new Date(2026, 10, 18, 7, 30).toISOString()
    render(
      <TeamChat
        chat={{
          state: 'connected',
          connector: 'matrix',
          talkoot: 'crew',
          outbound: {
            held: [
              { text: 'tests are running', at },
              { from: 'jev', text: 'all green, merged', at, reason: 'finished' },
            ],
            more: 3,
            quiet: true,
            quiet_ends: ends,
            next_brief: brief,
          },
        }}
      />,
    )
    expect(screen.getByText('Quiet hours until 07:00. A question, an approval, and a reply to you still go out.')).toBeTruthy()
    expect(screen.getByText('5 messages held back from chat')).toBeTruthy()
    const rows = screen.getAllByRole('listitem').map((li) => li.textContent)
    expect(rows).toEqual(['for the brief 23:05 tests are running', 'finished work 23:05 jev: all green, merged'])
    expect(screen.getByText('The next brief goes out at 07:30.')).toBeTruthy()
  })

  it('counts one held message in the singular', () => {
    const at = new Date(2026, 10, 17, 23, 5).toISOString()
    render(<TeamChat chat={{ state: 'connected', connector: 'matrix', talkoot: 'crew', outbound: { held: [{ text: 'tests are running', at }] } }} />)
    expect(screen.getByText('1 message held back from chat')).toBeTruthy()
  })

  it('says that the brief follows the next message when no brief time is set', () => {
    render(<TeamChat chat={{ state: 'connected', connector: 'matrix', talkoot: 'crew', outbound: { brief_on_reply: true } }} />)
    expect(screen.getByText('The brief goes out when you next write in chat.')).toBeTruthy()
    expect(screen.queryByText(/held back/)).toBeNull()
    expect(screen.queryByText(/Quiet hours/)).toBeNull()
  })

  it('says that a bridge is still connecting', () => {
    render(<TeamChat chat={{ state: 'connecting', connector: 'matrix', talkoot: 'crew' }} />)
    expect(screen.getByText('Chat bridge connecting: matrix')).toBeTruthy()
  })
})
