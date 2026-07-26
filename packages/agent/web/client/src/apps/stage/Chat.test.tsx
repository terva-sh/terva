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
    render(<Chat client={client} sessionId="s1" onBack={() => {}} onOpenSession={() => {}} onOpenStudio={() => {}} />)
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

  // Editing is behind the row's ✎ now; clicking the message is for reading it.
  function openEditor(text: string) {
    const row = screen.getByText(text).closest('.stage-row') as HTMLElement
    fireEvent.click(row.querySelector('.stage-msgedit') as HTMLElement)
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
    render(<Chat client={client} sessionId="s1" onBack={() => {}} onOpenSession={() => {}} onOpenStudio={() => {}} />)

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
    render(<Chat client={client} sessionId="s1" onBack={() => {}} onOpenSession={() => {}} onOpenStudio={() => {}} />)
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

// Editing a message lives behind ✎, not behind clicking the message.
//
// The bubble used to open the editor on any click, so wanting to copy a line
// meant the message turned into a textarea under your hands. This is the CI-
// gated half of that change; the smoke suite (stage-message-edit) covers what
// only a real browser can — that a selection drag survives.
describe('Chat message editing', () => {
  afterEach(() => {
    cleanup()
    vi.useRealTimers()
    vi.unstubAllGlobals()
  })

  function mountWithScene() {
    vi.useFakeTimers()
    const client = fakeClient()
    render(<Chat client={client} sessionId="s1" onBack={() => {}} onOpenSession={() => {}} onOpenStudio={() => {}} />)
    act(() => {
      client.emit('s1', {
        type: 'snapshot',
        snapshot: {
          epoch: 7,
          base: 0,
          total: 3,
          busy: false,
          messages: [
            { role: 'user', content: [{ type: 'text', text: 'i step inside' }] },
            { role: 'assistant', content: [{ type: 'text', text: 'the door shuts' }] },
            { role: 'assistant', content: [{ type: 'text', text: 'she looks up' }] },
          ],
        },
      } as unknown as WireEvent)
      vi.advanceTimersByTime(64)
    })
    return client
  }

  const editors = () => document.querySelectorAll('.stage-edit textarea')

  it('does not open the editor when the message itself is clicked', () => {
    mountWithScene()
    fireEvent.click(screen.getByText('the door shuts'))
    expect(editors()).toHaveLength(0)
  })

  it('opens the editor from ✎', () => {
    mountWithScene()
    fireEvent.click(document.querySelectorAll('.stage-msgedit')[1] as HTMLElement)
    expect(editors()).toHaveLength(1)
    expect((editors()[0] as HTMLTextAreaElement).value).toBe('the door shuts')
  })

  it('offers ✎ on every message, not only the last', () => {
    // The generation controls (↻, ↻✎, ⤸) belong to the last reply alone, which
    // is why the bar could not simply be reused as-is: editing has to reach a
    // message anywhere in the scene, including your own.
    mountWithScene()
    expect(document.querySelectorAll('.stage-msgedit')).toHaveLength(3)
    expect(document.querySelectorAll('.stage-row--user .stage-msgedit')).toHaveLength(1)
    expect(screen.getAllByTitle('Regenerate')).toHaveLength(1)
  })

  it('drops the row\'s ✎ while that row is being edited', () => {
    // Its own bar would sit under the open edit box offering to open it again.
    //
    // BOTH row kinds are checked because each renders its own bar with its own
    // guard: a version of this test that only edited the user row let a mutation
    // through that dropped the assistant row's `!editing` entirely.
    mountWithScene()
    const pencils = () => document.querySelectorAll('.stage-msgedit')

    fireEvent.click(pencils()[0] as HTMLElement) // your own message
    expect(pencils()).toHaveLength(2)
    fireEvent.click(screen.getByText('Cancel'))

    fireEvent.click(pencils()[1] as HTMLElement) // a reply, mid-scene
    expect(pencils()).toHaveLength(2)
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
    render(<Chat client={client} sessionId="s1" onBack={() => {}} onOpenSession={() => {}} onOpenStudio={() => {}} />)
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
    render(<Chat client={client} sessionId="s1" onBack={() => {}} onOpenSession={() => {}} onOpenStudio={() => {}} />)
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
  //
  // The second half of that is no longer possible — the bubble opens nothing on
  // any click (see "Chat message editing") — so this asserts the copy alone
  // rather than keeping a guard that cannot fail.
  it('copies a code block', async () => {
    const writeText = vi.fn().mockResolvedValue(undefined)
    vi.stubGlobal('navigator', { clipboard: { writeText } })
    mountWith([{ role: 'assistant', content: [{ type: 'text', text: '```\nls -la\n```' }] }])

    const button = document.querySelector('.code-copy') as HTMLElement
    expect(button).not.toBeNull()
    await act(async () => {
      fireEvent.click(button)
    })

    expect(writeText).toHaveBeenCalledWith('ls -la\n')
  })
})

// Reaching the character card from inside a scene: the header portrait opens the
// same detail sheet → editor the Library grid does, so you don't back out of the
// chat to inspect or edit the card you're playing. The gate matters — a creator
// or plain-persona chat has no card, and the affordance must not appear then.
describe('Chat character-card access from the header', () => {
  const onOpenStudio = vi.fn()
  afterEach(() => {
    cleanup()
    vi.useRealTimers()
    onOpenStudio.mockReset()
  })

  const card = {
    id: 'kobeni-abc',
    name: 'Kobeni',
    avatar_url: 'data:image/png;base64,AAAA',
    spec_version: '2.0',
    greetings: 1,
    raw: { spec: 'chara_card_v2', spec_version: '2.0', data: { description: 'a nervous new hire', first_mes: 'h-hi' } },
  }

  function mount(session: Record<string, unknown>) {
    vi.useFakeTimers()
    const client = fakeClient({
      respond: (method) => {
        if (method === 'cards.get') return card
        if (method === 'cards.lint') return { findings: [] }
        return {}
      },
    })
    render(<Chat client={client} sessionId="s1" onBack={() => {}} onOpenSession={() => {}} onOpenStudio={onOpenStudio} />)
    act(() => {
      client.emit('s1', {
        type: 'snapshot',
        snapshot: {
          epoch: 1,
          base: 0,
          total: 1,
          busy: false,
          session,
          messages: [{ role: 'assistant', content: [{ type: 'text', text: 'h-hi' }] }],
        },
      } as unknown as WireEvent)
      vi.advanceTimersByTime(64)
    })
    return client
  }

  // The card resolves via an async cards.get; flush the microtasks it rides.
  const flush = () =>
    act(async () => {
      await Promise.resolve()
      await Promise.resolve()
    })

  it('opens the bound character’s card — view, then hands editing to the studio', async () => {
    mount({ id: 's1', experience: 'chat', card: 'kobeni-abc' })
    await flush()
    const open = document.querySelector('.stage-chat__cardbtn') as HTMLElement | null
    expect(open).not.toBeNull()
    // The tooltip leads with the character name, which the header may truncate.
    expect(open!.getAttribute('title')).toBe('Kobeni — view or edit their card')

    fireEvent.click(open!)
    await flush()
    // The detail sheet, not the editor.
    expect(document.querySelector('.stage-sheet--detail:not(.stage-cardeditor)')).not.toBeNull()

    // ✎ Edit LEAVES the scene for the character studio rather than opening an
    // editor over it. The editor mounted here is how a save could tear itself
    // down mid-consultation: two hosts, two ideas of when it should close.
    fireEvent.click(screen.getByText('✎ Edit'))
    await flush()
    expect(document.querySelector('.stage-cardeditor')).toBeNull()
    expect(onOpenStudio).toHaveBeenCalledTimes(1)
    expect(onOpenStudio.mock.calls[0][0]?.card?.id).toBe('kobeni-abc')
    expect(onOpenStudio.mock.calls[0][0]?.tab).toBe('character')
    // And the sheet closed behind it, so returning does not land on a stale one.
    expect(document.querySelector('.stage-sheet--detail')).toBeNull()
  })

  it('shows no card affordance on a card-less session, but keeps the model chip', async () => {
    mount({ id: 's1', experience: 'chat', persona: 'kartoittaja' })
    await flush()
    expect(document.querySelector('.stage-chat__cardbtn')).toBeNull()
    // The header model chip is unconditional — every session picks a model.
    expect(document.querySelector('.stage-modelpick--compact')).not.toBeNull()
  })

  // Who you are in the scene, beside who you are playing with. It was steerable
  // only from a tab inside the drawer, so the answer to "who am I here" was
  // invisible until you went looking for it.
  it('names who you are playing as, and hands the You tab the scene', async () => {
    mount({ id: 's1', experience: 'chat', card: 'kobeni-abc', user_name: 'Kira', user_description: 'A wary courier.', user_pronouns: 'she/her' })
    await flush()
    const chip = document.querySelector('.stage-chat__playingas') as HTMLElement | null
    expect(chip).not.toBeNull()
    expect(chip!.textContent).toContain('Kira')
    expect(chip!.className).not.toContain('--unset')

    fireEvent.click(chip!)
    await flush()
    expect(onOpenStudio).toHaveBeenCalledTimes(1)
    // The You tab, carrying the scene it was opened from — without the session
    // the tab would edit the saved library instead of steering this chat.
    expect(onOpenStudio.mock.calls[0][0]?.tab).toBe('you')
    expect(onOpenStudio.mock.calls[0][0]?.scene).toMatchObject({
      session: 's1',
      name: 'Kira',
      description: 'A wary courier.',
      pronouns: 'she/her',
    })
  })

  // An unset persona is the case worth surfacing: the scene addresses you as the
  // literal "User" and nothing else says so.
  it('invites a persona when the scene has none', async () => {
    mount({ id: 's1', experience: 'chat', card: 'kobeni-abc' })
    await flush()
    const chip = document.querySelector('.stage-chat__playingas') as HTMLElement | null
    expect(chip).not.toBeNull()
    expect(chip!.className).toContain('stage-chat__playingas--unset')
  })

  // ...but only where there IS a you to play: user.bind refuses a coding session,
  // and the cartographer's workshop is a conversation ABOUT a story.
  it('offers no persona on the creator workshop or a coding session', async () => {
    mount({ id: 's1', experience: 'chat', persona: 'kartoittaja' })
    await flush()
    expect(document.querySelector('.stage-chat__playingas')).toBeNull()
    cleanup()

    mount({ id: 's1' })
    await flush()
    expect(document.querySelector('.stage-chat__playingas')).toBeNull()
  })
})
