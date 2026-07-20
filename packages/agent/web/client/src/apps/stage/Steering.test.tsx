// @vitest-environment happy-dom
import { afterEach, describe, expect, it, vi } from 'vitest'
import { fakeClient } from '../../platform/ctrlproto/testing'
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/preact'
import type { SessionInfo } from '../../platform/ctrlproto/types'
import { Steering } from './Steering'

// backgrounds.delete was served by the daemon and called by nothing. These pin
// the two things the DAEMON does not do, which is the whole reason the client
// has to: it never checks whether a backdrop is in use, and it never clears a
// session's binding.
const BACKGROUNDS = [
  { id: 'bg-alpha', url: '/media/backgrounds/bg-alpha' },
  { id: 'bg-beta', url: '/media/backgrounds/bg-beta' },
]

function mountScene(info: Partial<SessionInfo>) {
  const client = fakeClient({
    respond: (method) => (method === 'backgrounds.list' ? { backgrounds: BACKGROUNDS } : {}),
  })
  render(
    <Steering
      client={client}
      sessionId="s1"
      info={{ id: 's1', experience: 'chat', ...info } as SessionInfo}
      onClose={() => {}}
    />,
  )
  fireEvent.click(screen.getByText('Scene'))
  return client
}

const sentMethods = (c: { send: ReturnType<typeof vi.fn> }) => c.send.mock.calls.map((call) => call[0] as string)

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
})

describe('Steering — delete a backdrop', () => {
  it('offers a remove control per backdrop and deletes on confirm', async () => {
    const client = mountScene({ background: 'bg-alpha' })
    vi.stubGlobal('confirm', vi.fn().mockReturnValue(true))

    const dels = await screen.findAllByTitle('Delete this backdrop')
    // One per backdrop — the "None" tile is not a backdrop and must not get one.
    expect(dels).toHaveLength(2)

    fireEvent.click(dels[1])
    await waitFor(() => expect(sentMethods(client)).toContain('backgrounds.delete'))
    // No session arg: the background STORE is workspace-global (only the binding
    // is per-session), so the verb carries no `sess`.
    expect(client.send).toHaveBeenCalledWith('backgrounds.delete', { id: 'bg-beta' })
  })

  it('does nothing when the confirm is declined', async () => {
    const client = mountScene({ background: 'bg-alpha' })
    vi.stubGlobal('confirm', vi.fn().mockReturnValue(false))

    fireEvent.click((await screen.findAllByTitle('Delete this backdrop'))[0])
    await waitFor(() => expect(client.send).toHaveBeenCalled())
    expect(sentMethods(client)).not.toContain('backgrounds.delete')
  })

  // The daemon leaves SessionMeta.Background pointing at bytes that no longer
  // exist, and a dangling /media URL fails as a silent CSS background-image — the
  // scene just goes bare. Unbinding is the client's job, and it must happen
  // BEFORE the delete so a failed delete leaves us unbound rather than bound to
  // nothing.
  it('unbinds first when deleting the backdrop this chat is on', async () => {
    const client = mountScene({ background: 'bg-alpha' })
    vi.stubGlobal('confirm', vi.fn().mockReturnValue(true))

    fireEvent.click((await screen.findAllByTitle('Delete this backdrop'))[0])
    await waitFor(() => expect(sentMethods(client)).toContain('backgrounds.delete'))

    const order = sentMethods(client)
    expect(order.indexOf('backgrounds.bind')).toBeLessThan(order.indexOf('backgrounds.delete'))
    expect(client.send).toHaveBeenCalledWith('backgrounds.bind', { background: '' }, 's1')
  })

  // ...but only then. Unbinding on every delete would clear a scene the user
  // never touched, which is a worse bug than the one being fixed.
  it('leaves the binding alone when deleting a different backdrop', async () => {
    const client = mountScene({ background: 'bg-alpha' })
    vi.stubGlobal('confirm', vi.fn().mockReturnValue(true))

    fireEvent.click((await screen.findAllByTitle('Delete this backdrop'))[1])
    await waitFor(() => expect(sentMethods(client)).toContain('backgrounds.delete'))
    expect(sentMethods(client)).not.toContain('backgrounds.bind')
  })
})

// The World tab is where someone comes looking for "what is in this world's
// context". It offered to pin a scene-state card while none existed and then said
// nothing once one did — so the tab denied knowing about the card at exactly the
// moment it existed. That silence was the only thing an author saw after accepting
// the doctor's scene_state proposal, because the accept happens behind the
// drawer's own full-screen backdrop, which covers the card above the composer.
describe('Steering — the World tab accounts for the scene-state pin', () => {
  function mountWorld(info: Partial<SessionInfo>) {
    const client = fakeClient({
      respond: (method) => (method === 'backgrounds.list' ? { backgrounds: BACKGROUNDS } : {}),
    })
    render(
      <Steering
        client={client}
        sessionId="s1"
        info={{ id: 's1', experience: 'chat', ...info } as SessionInfo}
        onClose={() => {}}
      />,
    )
    fireEvent.click(screen.getByText('World'))
    return client
  }

  it('offers to pin one when nothing is pinned', () => {
    mountWorld({ world_lore: [] })
    expect(screen.queryByText('📌 Pin a scene-state card')).not.toBeNull()
  })

  it('shows the pinned card instead of going silent, and says where to edit it', () => {
    mountWorld({
      world_lore: [{ name: 'Scene state', constant: true, content: 'Day 14, first light.' }],
    } as Partial<SessionInfo>)

    // The offer is gone (there is one card, and it exists)...
    expect(screen.queryByText('📌 Pin a scene-state card')).toBeNull()
    // ...replaced by the card's actual content, not a bare label.
    expect(screen.queryByText('Day 14, first light.')).not.toBeNull()
    // And it points at the one editor, so this read-only view is not a dead end.
    expect(screen.queryByText(/Pinned above the composer/)).not.toBeNull()
  })

  // The pin has its own surface; listing it again under World lore would give one
  // card two rows, with the second carrying edit/delete controls that fight the
  // card's own.
  it('does not also list the pin among ordinary world lore', () => {
    mountWorld({
      world_lore: [
        { name: 'Scene state', constant: true, content: 'Day 14, first light.' },
        { name: 'The Marrow debt', constant: true, content: '3 silver, owed since the spring fair.' },
      ],
    } as Partial<SessionInfo>)

    expect(screen.queryAllByText('Day 14, first light.')).toHaveLength(1)
    expect(screen.queryByText('3 silver, owed since the spring fair.')).not.toBeNull()
  })
})
