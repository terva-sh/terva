// @vitest-environment happy-dom
import { afterEach, describe, expect, it, vi } from 'vitest'
import { act, cleanup, fireEvent, render, renderHook, screen } from '@testing-library/preact'
import type { Client } from '../../platform/ctrlproto/client'
import type { WireEvent } from '../../platform/ctrlproto/types'
import { Chat, deleteWarning } from './Chat'
import { useConversation } from './useConversation'

describe('deleteWarning', () => {
  // Deleting the last message is a clean rewind. Deleting one with a downstream
  // is a different act — the replies below were written to something that is
  // about to vanish and they STAY — and the prompt is the only place the user
  // finds that out, since the daemon splices without complaint.
  it('promises nothing about a downstream when there is none', () => {
    const msg = deleteWarning(0)
    expect(msg).toMatch(/can’t be undone/)
    expect(msg).not.toMatch(/written to it/)
  })

  it('names the downstream that will be left behind', () => {
    const msg = deleteWarning(4)
    expect(msg).toContain('4 replies were written to it')
    expect(msg).toMatch(/may not read straight/)
    // Branch is the non-destructive alternative and the moment to say so.
    expect(msg).toMatch(/branch/i)
  })

  it('agrees with itself about a single reply', () => {
    expect(deleteWarning(1)).toContain('1 reply was written to it')
  })

  // A negative can only come from an index/count mismatch; it must not render as
  // "-1 replies", which reads as a bug to the user at the exact moment they are
  // being asked to approve something irreversible.
  it('treats an impossible count as no downstream', () => {
    expect(deleteWarning(-1)).toBe(deleteWarning(0))
  })
})

describe('useConversation deleteAt', () => {
  // The daemon has served message.delete since the transcript-revision work
  // landed; no client had ever called it. These pin the call it now makes,
  // because a wrong verb name or a dropped epoch fails silently at runtime —
  // the daemon answers with an error nobody is watching for.
  function stubClient() {
    const send = vi.fn().mockResolvedValue({})
    return { send, fire: vi.fn(), onEvent: undefined } as unknown as Client & { send: ReturnType<typeof vi.fn> }
  }

  it('sends message.delete with the index, on the session', () => {
    const client = stubClient()
    const { result } = renderHook(() => useConversation(client, 'sess-1', 0))
    result.current.deleteAt(3)
    expect(client.send).toHaveBeenCalledWith('message.delete', { epoch: 0, index: 3 }, 'sess-1')
  })

  // The epoch is what makes a concurrent revision safe: the daemon rejects a
  // delete carrying a stale one rather than applying it to a shifted index. A
  // delete sent without it would be accepted against the wrong message.
  it('carries the epoch, not a hardcoded zero', () => {
    const client = stubClient()
    const { result } = renderHook(() => useConversation(client, 'sess-1', 0))
    result.current.deleteAt(0)
    const params = client.send.mock.calls[0][1] as Record<string, unknown>
    expect(params).toHaveProperty('epoch')
    expect(Object.keys(params).sort()).toEqual(['epoch', 'index'])
  })
})

// Driving the real screen, not the hook. The hook tests above pin the CALL; they
// cannot see a Delete button wired to nothing, gated on the wrong row kind, or
// firing past a declined confirm — and a test that drives the hook cannot see a
// bug in the button, the same way a test that drives the renderer cannot see a
// bug in a gate.
describe('Chat delete affordance', () => {
  afterEach(() => {
    cleanup()
    vi.useRealTimers()
    vi.unstubAllGlobals()
  })

  // Mount the chat and feed it one snapshot: a user line, a reply, a follow-up.
  // The pacer holds `snapshot` behind a tick (it is an ORDERED event), so the
  // timers have to run before anything renders.
  function mountChat() {
    vi.useFakeTimers()
    const send = vi.fn().mockResolvedValue({})
    const client = { send, fire: vi.fn(), onEvent: undefined } as unknown as Client & {
      send: ReturnType<typeof vi.fn>
      onEvent?: (sess: string, ev: WireEvent) => void
    }
    render(<Chat client={client} sessionId="s1" onBack={() => {}} onOpenSession={() => {}} />)
    const snapshot: WireEvent = {
      type: 'snapshot',
      snapshot: {
        epoch: 7,
        base: 0,
        total: 3,
        busy: false,
        messages: [
          { role: 'user', content: [{ type: 'text', text: 'i step inside' }] },
          { role: 'assistant', content: [{ type: 'text', text: 'the door shuts' }] },
          { role: 'user', content: [{ type: 'text', text: 'i turn around' }] },
        ],
      },
    } as unknown as WireEvent
    act(() => {
      client.onEvent?.('s1', snapshot)
      vi.advanceTimersByTime(64)
    })
    return client
  }

  function openEditor(text: string) {
    fireEvent.click(screen.getByText(text))
    return screen.getByTitle('Delete this message')
  }

  it('offers Delete on a message, and asks before removing it', () => {
    const client = mountChat()
    const confirm = vi.fn().mockReturnValue(true)
    vi.stubGlobal('confirm', confirm)

    fireEvent.click(openEditor('the door shuts'))

    expect(confirm).toHaveBeenCalledOnce()
    // Index 1 of the window, carrying the snapshot's epoch — not a fresh 0.
    expect(client.send).toHaveBeenCalledWith('message.delete', { epoch: 7, index: 1 }, 's1')
    // And the editor closes. Everything below shifts up by one when the snapshot
    // lands, so an edit box left open on `idx` would be pointing at the message
    // that took the deleted one's place — a different message than the one the
    // user opened, holding the text of the one they just removed.
    expect(screen.queryByTitle('Delete this message')).toBeNull()
  })

  it('does nothing when the confirm is declined', () => {
    const client = mountChat()
    vi.stubGlobal('confirm', vi.fn().mockReturnValue(false))

    fireEvent.click(openEditor('the door shuts'))

    expect(client.send).not.toHaveBeenCalledWith('message.delete', expect.anything(), expect.anything())
    // The edit box stays open: declining is "no, not that" — being kicked back to
    // the transcript would read as though something happened.
    expect(screen.getByTitle('Delete this message')).toBeTruthy()
  })

  // The last message has nothing written to it, so its confirm must not claim a
  // downstream. This is the arithmetic in the component rather than the helper —
  // an off-by-one here shows the user the wrong warning about the wrong thing.
  it('warns about the downstream only when there is one', () => {
    mountChat()
    const confirm = vi.fn().mockReturnValue(false)
    vi.stubGlobal('confirm', confirm)

    fireEvent.click(openEditor('i turn around'))
    expect(confirm.mock.calls[0][0]).toBe(deleteWarning(0))

    fireEvent.click(screen.getByText('Cancel'))
    fireEvent.click(openEditor('i step inside'))
    expect(confirm.mock.calls[1][0]).toBe(deleteWarning(2))
  })
})
