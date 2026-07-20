// @vitest-environment happy-dom
import { afterEach, describe, expect, it, vi } from 'vitest'
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/preact'
import type { Client } from '../../platform/ctrlproto/client'
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
  const send = vi.fn().mockImplementation((method: string) => {
    if (method === 'backgrounds.list') return Promise.resolve({ backgrounds: BACKGROUNDS })
    return Promise.resolve({})
  })
  const client = { send, fire: vi.fn() } as unknown as Client & { send: ReturnType<typeof vi.fn> }
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
