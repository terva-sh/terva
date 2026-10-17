// @vitest-environment happy-dom
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/preact'
import { afterEach, describe, expect, it } from 'vitest'
import { fakeClient } from '../../platform/ctrlproto/testing'
import type { Verb } from '../../platform/ctrlproto/types'
import { Recruit } from './Recruit'
import { who } from './RoomLine'

afterEach(cleanup)

describe('the recruiter', () => {
  it('opens a recruiter session for the open team and links to it', async () => {
    const client = fakeClient({
      respond: (method: Verb) => (method === 'talkoot.recruit' ? { session: { id: '20261015-120000-abcdef01' } } : {}),
    })
    render(<Recruit client={client} id="crew" onError={() => {}} />)
    fireEvent.click(screen.getByText('Find a member'))
    const link = (await screen.findByText('Open the recruiter')) as HTMLAnchorElement
    expect(client.commands.find((c) => c.method === 'talkoot.recruit')?.params).toEqual({ id: 'crew' })
    expect(link.getAttribute('href')).toBe('/?session=20261015-120000-abcdef01')
  })

  it('reports a refusal and keeps the button', async () => {
    let error = ''
    const client = fakeClient({
      respond: (method: Verb) => {
        if (method === 'talkoot.recruit') throw new Error('steer refused')
        return {}
      },
    })
    render(<Recruit client={client} id="crew" onError={(e) => (error = e)} />)
    fireEvent.click(screen.getByText('Find a member'))
    await waitFor(() => expect(error).toContain('steer refused'))
    expect(screen.getByText('Find a member')).toBeTruthy()
  })

  it('forgets the recruiter when the team changes, and reports a reply with no session', async () => {
    let error = ''
    let answer: unknown = { session: { id: '20261015-120000-abcdef01' } }
    const client = fakeClient({ respond: (method: Verb) => (method === 'talkoot.recruit' ? answer : {}) })
    const { rerender } = render(<Recruit client={client} id="crew" onError={(e) => (error = e)} />)
    fireEvent.click(screen.getByText('Find a member'))
    await screen.findByText('Open the recruiter')
    rerender(<Recruit client={client} id="docs" onError={(e) => (error = e)} />)
    await waitFor(() => expect(screen.queryByText('Open the recruiter')).toBeNull())
    answer = {}
    fireEvent.click(screen.getByText('Find a member'))
    await waitFor(() => expect(error).toBe('The daemon opened no recruiter session.'))
    expect(screen.queryByText('Open the recruiter')).toBeNull()
  })

  it('names a recruiter proposer in words, not by its session id', () => {
    expect(who('recruiter:20261015-120000-abcdef01')).toBe('the recruiter')
    expect(who('human:sothr')).toBe('sothr')
    expect(who('lead')).toBe('lead')
  })
})
