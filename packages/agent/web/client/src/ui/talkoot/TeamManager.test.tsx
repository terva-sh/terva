// @vitest-environment happy-dom
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/preact'
import { afterEach, describe, expect, it } from 'vitest'
import { fakeClient } from '../../platform/ctrlproto/testing'
import type { Verb } from '../../platform/ctrlproto/types'
import { who } from './RoomLine'
import { TeamManager } from './TeamManager'

afterEach(cleanup)

describe('the team manager', () => {
  it('opens the manager session and links to it', async () => {
    const client = fakeClient({
      respond: (method: Verb) => (method === 'talkoot.manager' ? { session: { id: '20261015-120000-abcdef01', manager: true } } : {}),
    })
    render(<TeamManager client={client} />)
    fireEvent.click(screen.getByText('Team manager'))
    const link = (await screen.findByText('Open the team manager')) as HTMLAnchorElement
    expect(client.commands.some((c) => c.method === 'talkoot.manager')).toBe(true)
    expect(link.getAttribute('href')).toBe('/?session=20261015-120000-abcdef01')
  })

  it('reports a refusal and keeps the button', async () => {
    const client = fakeClient({
      respond: (method: Verb) => {
        if (method === 'talkoot.manager') throw new Error('steer refused')
        return {}
      },
    })
    render(<TeamManager client={client} />)
    fireEvent.click(screen.getByText('Team manager'))
    await screen.findByText('steer refused')
    expect(screen.getByText('Team manager')).toBeTruthy()
  })

  it('reports a reply with no session', async () => {
    const client = fakeClient({ respond: () => ({}) })
    render(<TeamManager client={client} primary />)
    fireEvent.click(screen.getByText('Team manager'))
    await waitFor(() => expect(screen.getByText('The daemon opened no team manager session.')).toBeTruthy())
    expect(screen.queryByText('Open the team manager')).toBeNull()
  })

  it('names the manager proposer in words, not by its session id', () => {
    expect(who('manager:20261015-120000-abcdef01')).toBe('the team manager')
  })
})
