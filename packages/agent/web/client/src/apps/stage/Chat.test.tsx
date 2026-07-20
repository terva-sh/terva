// @vitest-environment happy-dom
import { afterEach, describe, expect, it, vi } from 'vitest'
import { fakeClient } from '../../platform/ctrlproto/testing'
import { act, cleanup, fireEvent, render, renderHook, screen } from '@testing-library/preact'
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
  const stubClient = () => fakeClient()

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
    const client = fakeClient()
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
      client.emit('s1', snapshot)
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

// Does the pinned scene-state card appear the moment it is set, with no turn and
// no other traffic in between?
//
// Setting it — the doctor's accepted scene_state proposal, the model's world_note,
// Steering's pin button — all land on world.lore.put, which broadcasts a fresh
// snapshot carrying session.world_lore. If that snapshot alone does not raise the
// card, the pin would look like it did nothing until some LATER event happened to
// refresh the session info, which is exactly the "it showed up after a round of
// suggest" report this is here to rule in or out.
describe('Chat scene-state card liveness', () => {
  afterEach(() => {
    cleanup()
    vi.useRealTimers()
  })

  function snapshotWith(lore: unknown[]): WireEvent {
    return {
      type: 'snapshot',
      snapshot: {
        epoch: 7,
        base: 0,
        total: 1,
        busy: false,
        session: { id: 's1', experience: 'chat', world_lore: lore },
        messages: [{ role: 'assistant', content: [{ type: 'text', text: 'the door shuts' }] }],
      },
    } as unknown as WireEvent
  }

  it('raises the card on the put snapshot alone — no turn, no suggest', () => {
    vi.useFakeTimers()
    const client = fakeClient()
    render(<Chat client={client} sessionId="s1" onBack={() => {}} onOpenSession={() => {}} />)

    // Nothing pinned yet.
    act(() => {
      client.emit('s1', snapshotWith([]))
      vi.advanceTimersByTime(64)
    })
    expect(screen.queryByText('Day 14, first light.')).toBeNull()

    // The put's broadcast, and nothing else.
    act(() => {
      client.emit(
        's1',
        snapshotWith([{ name: 'Scene state', constant: true, content: 'Day 14, first light.\n3 silver owed to Marrow.' }]),
      )
      vi.advanceTimersByTime(64)
    })
    expect(screen.queryByText('Day 14, first light.')).not.toBeNull()
  })
})

// ↻ is the most-used control on the surface and nothing pinned that it actually
// sends anything. The guided twin beside it made the handler take arguments,
// which is exactly the kind of change that can turn a working button into a
// silent no-op without any test noticing.
describe('Chat regenerate controls', () => {
  afterEach(() => {
    cleanup()
    vi.useRealTimers()
    vi.unstubAllGlobals()
  })

  function mountWithReply() {
    vi.useFakeTimers()
    const client = fakeClient()
    render(<Chat client={client} sessionId="s1" onBack={() => {}} onOpenSession={() => {}} />)
    act(() => {
      client.emit('s1', {
        type: 'snapshot',
        snapshot: {
          epoch: 7,
          base: 0,
          total: 2,
          busy: false,
          messages: [
            { role: 'user', content: [{ type: 'text', text: 'i step inside' }] },
            { role: 'assistant', content: [{ type: 'text', text: 'the door shuts' }] },
          ],
        },
      } as unknown as WireEvent)
      vi.advanceTimersByTime(64)
    })
    return client
  }

  it('sends turn.retry when ↻ is clicked', () => {
    const client = mountWithReply()
    fireEvent.click(screen.getByTitle('Regenerate'))
    expect(client.send).toHaveBeenCalledWith('turn.retry', { epoch: 7 }, 's1')
  })

  it('sends the guidance and the prior-take choice when the guided box is submitted', () => {
    const client = mountWithReply()
    fireEvent.click(screen.getByTitle('Regenerate with guidance'))
    const box = screen.getByPlaceholderText('What should be different this time?')
    fireEvent.input(box, { target: { value: 'make her answer out loud' } })
    fireEvent.click(screen.getByText('Regenerate'))
    expect(client.send).toHaveBeenCalledWith(
      'turn.retry',
      { epoch: 7, guidance: 'make her answer out loud', ignore_prior: false },
      's1',
    )
  })
})

// A refused action has to report where the user is looking. The transcript pins
// itself to the bottom, so an error rendered INSIDE it — as this one was, as the
// first child — scrolls out of view the instant a scene gets long. The daemon's
// refusal was reaching the client and being drawn hundreds of messages above the
// button that caused it, which is indistinguishable from a dead button.
describe('Chat surfaces a refusal where it happened', () => {
  afterEach(() => {
    cleanup()
    vi.useRealTimers()
  })

  it('renders a rejected retry outside the scrolling transcript', async () => {
    vi.useFakeTimers()
    const client = fakeClient({
      respond: () => {
        throw new Error('nothing to retry')
      },
    })
    render(<Chat client={client} sessionId="s1" onBack={() => {}} onOpenSession={() => {}} />)
    act(() => {
      client.emit('s1', {
        type: 'snapshot',
        snapshot: {
          epoch: 7,
          base: 0,
          total: 2,
          busy: false,
          messages: [
            { role: 'user', content: [{ type: 'text', text: 'i step inside' }] },
            { role: 'assistant', content: [{ type: 'text', text: 'the door shuts' }] },
          ],
        },
      } as unknown as WireEvent)
      vi.advanceTimersByTime(64)
    })

    fireEvent.click(screen.getByTitle('Regenerate'))
    await act(async () => {
      await Promise.resolve()
      vi.advanceTimersByTime(64)
    })

    const banner = screen.getByText(/nothing to retry/)
    expect(banner).not.toBeNull()
    // The actual regression: it must not live inside the element that scrolls.
    expect(banner.closest('.stage-transcript')).toBeNull()
  })
})

// Stage renders its own transcript rows rather than the panel's MessageContent,
// so everything the panel's row does had to be re-done here — and three things
// silently were not. These pin the ones that were user-visible: the wire carried
// the data and Stage dropped it on the floor.
describe('Chat transcript fidelity', () => {
  afterEach(() => {
    cleanup()
    vi.useRealTimers()
    vi.unstubAllGlobals()
  })

  // A 1x1 transparent PNG — enough to assert the src is built and rendered.
  const PNG =
    'iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg=='

  function mountWith(messages: unknown[]) {
    vi.useFakeTimers()
    const client = fakeClient()
    render(<Chat client={client} sessionId="s1" onBack={() => {}} onOpenSession={() => {}} />)
    act(() => {
      client.emit('s1', {
        type: 'snapshot',
        snapshot: { epoch: 1, base: 0, total: messages.length, busy: false, messages },
      } as unknown as WireEvent)
      vi.advanceTimersByTime(64)
    })
    return client
  }

  // Stage negotiates image-data over the shared client and the shared store
  // attaches the blocks to the item — so the payload always arrived. The row
  // just never read item.images, and a generated picture rendered as nothing.
  it('renders an image the assistant sent', () => {
    mountWith([
      {
        role: 'assistant',
        content: [
          { type: 'text', text: 'here it is' },
          { type: 'image', mime_type: 'image/png', data: PNG },
        ],
      },
    ])
    const img = document.querySelector('img.msg-image') as HTMLImageElement | null
    expect(img).not.toBeNull()
    expect(img?.src).toBe(`data:image/png;base64,${PNG}`)
  })

  // Same for a tool that RETURNED an image (generate_image, a backdrop). Stage
  // folds tool calls to one quiet line, but the picture is the point of the call.
  it('renders an image a tool returned, without unfolding the tool row', () => {
    mountWith([
      {
        role: 'tool',
        content: [{ type: 'image', mime_type: 'image/png', data: PNG }],
        tool_call_id: 'c1',
        name: 'generate_image',
      },
    ])
    expect(document.querySelector('img.msg-image')).not.toBeNull()
  })

  // markdown.ts renders a .code-copy button into EVERY fenced block on every
  // surface. The click handler used to be private to the panel's timeline, so
  // in Stage the button was decoration: it copied nothing, and because it sits
  // inside the bubble, clicking it opened the inline editor instead.
  it('copies a code block, and does not open the editor doing it', async () => {
    const writeText = vi.fn().mockResolvedValue(undefined)
    vi.stubGlobal('navigator', { clipboard: { writeText } })
    mountWith([{ role: 'assistant', content: [{ type: 'text', text: '```\nls -la\n```' }] }])

    const button = document.querySelector('.code-copy') as HTMLElement
    expect(button).not.toBeNull()
    await act(async () => {
      fireEvent.click(button)
    })

    expect(writeText).toHaveBeenCalledWith('ls -la\n')
    // The bubble's tap-to-edit must not have fired underneath the copy.
    expect(document.querySelector('.stage-edit')).toBeNull()
  })

  // A tap anywhere else in the bubble still edits — the copy guard must not have
  // swallowed the row's own affordance.
  it('still opens the editor on a tap that is not the copy button', () => {
    mountWith([{ role: 'assistant', content: [{ type: 'text', text: 'the door shuts' }] }])
    fireEvent.click(screen.getByText('the door shuts'))
    expect(document.querySelector('.stage-edit')).not.toBeNull()
  })
})
