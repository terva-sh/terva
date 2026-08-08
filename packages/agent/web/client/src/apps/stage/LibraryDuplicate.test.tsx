// @vitest-environment happy-dom
//
// The Library's half of cards.duplicate: what it sends, and where it lands.
//
// CardDuplicate.test.tsx covers the name arithmetic and the sheet's control.
// Neither can see this layer's two interesting failures — a duplicate that sends
// the wrong id or an unnamed copy, and one that creates the card and then leaves
// you looking at the library wondering whether anything happened.
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/preact'
import { fakeClient } from '../../platform/ctrlproto/testing'
import type { Verb } from '../../platform/ctrlproto/types'
import { Library } from './Library'

const CARDS = [{ id: 'c1', name: 'Kobeni', greetings: 1 }]

function stub() {
  return fakeClient({
    respond: (method: Verb) => {
      switch (method) {
        case 'cards.list':
          return { cards: CARDS }
        case 'cards.get':
          return { id: 'c1', name: 'Kobeni', raw: { data: {} } }
        case 'cards.lint':
          return { findings: [] }
        case 'personas.list':
          return { personas: [] }
        case 'sessions.list':
          return { sessions: [] }
        case 'models.list':
          return { models: [] }
        case 'cards.duplicate':
          return { id: 'c2', name: 'Kobeni (copy)', greetings: 1 }
        default:
          return {}
      }
    },
  })
}

// Render the library and open Kobeni's detail sheet, where the action lives.
async function openSheet(onEditCharacter = () => {}) {
  const client = stub()
  render(<Library client={client} ready status="open" onOpenChat={() => {}} onEditCharacter={onEditCharacter} onEditYou={() => {}} onOpenWorld={() => {}} />)
  const grid = await waitFor(() => document.querySelector('.stage-grid') as HTMLElement)
  fireEvent.click(within(grid).getByTitle('Details'))
  await waitFor(() => expect(screen.getByText('⧉ Duplicate card')).toBeTruthy())
  return client
}

beforeEach(() => {
  const store = new Map<string, string>()
  vi.stubGlobal('localStorage', {
    getItem: (k: string) => store.get(k) ?? null,
    setItem: (k: string, v: string) => void store.set(k, String(v)),
    removeItem: (k: string) => void store.delete(k),
    clear: () => store.clear(),
    key: (i: number) => [...store.keys()][i] ?? null,
    get length() {
      return store.size
    },
  })
})

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
})

describe('duplicating a card from the library', () => {
  it('proposes a free name and sends what the author confirmed', async () => {
    // Typed with the arguments window.prompt actually receives: an
    // argument-less vi.fn infers an empty tuple, so mock.calls[0][1] —
    // the pre-filled default this test exists to check — cannot be indexed.
    const prompt = vi.fn((_message?: string, _default?: string) => 'Kobeni, braver')
    vi.stubGlobal('prompt', prompt)
    const client = await openSheet()

    fireEvent.click(screen.getByText('⧉ Duplicate card'))
    await waitFor(() => expect(client.send.mock.calls.some((c) => c[0] === 'cards.duplicate')).toBe(true))

    // Pre-filled with a name the daemon will accept: an unchanged name over
    // unchanged contents is the card being copied, and is refused.
    expect(prompt.mock.calls[0][1]).toBe('Kobeni (copy)')
    const call = client.send.mock.calls.find((c) => c[0] === 'cards.duplicate')
    expect(call?.[1]).toEqual({ id: 'c1', name: 'Kobeni, braver' })
  })

  it('lands in the editor on the COPY, not the original', async () => {
    vi.stubGlobal('prompt', () => 'Kobeni, braver')
    const onEditCharacter = vi.fn()
    await openSheet(onEditCharacter)

    fireEvent.click(screen.getByText('⧉ Duplicate card'))
    // A copy exists in order to be changed; stopping at the grid makes the
    // author hunt for the card they just made.
    await waitFor(() => expect(onEditCharacter).toHaveBeenCalled())
    expect(onEditCharacter.mock.calls[0][0].id, 'the editor must open the copy').toBe('c2')
  })

  it('sends nothing when the prompt is dismissed', async () => {
    vi.stubGlobal('prompt', () => null)
    const client = await openSheet()

    fireEvent.click(screen.getByText('⧉ Duplicate card'))
    await new Promise((r) => setTimeout(r, 0))
    expect(client.send.mock.calls.some((c) => c[0] === 'cards.duplicate')).toBe(false)
  })

  // An empty answer is a dismissal, not a request for an unnamed card: the
  // daemon would refuse it, and asking again is the author's move to make.
  it('sends nothing when the name is blanked out', async () => {
    vi.stubGlobal('prompt', () => '   ')
    const client = await openSheet()

    fireEvent.click(screen.getByText('⧉ Duplicate card'))
    await new Promise((r) => setTimeout(r, 0))
    expect(client.send.mock.calls.some((c) => c[0] === 'cards.duplicate')).toBe(false)
  })

  it('surfaces a refusal instead of pretending a copy was made', async () => {
    vi.stubGlobal('prompt', () => 'Kobeni')
    const onEditCharacter = vi.fn()
    const client = fakeClient({
      respond: (method: Verb) => {
        switch (method) {
          case 'cards.list':
            return { cards: CARDS }
          case 'cards.get':
            return { id: 'c1', name: 'Kobeni', raw: { data: {} } }
          case 'cards.lint':
            return { findings: [] }
          case 'cards.duplicate':
            throw new Error('badRequest: duplicate card: "Kobeni" is the name this card already has')
          default:
            return {}
        }
      },
    })
    render(<Library client={client} ready status="open" onOpenChat={() => {}} onEditCharacter={onEditCharacter} onEditYou={() => {}} onOpenWorld={() => {}} />)
    const grid = await waitFor(() => document.querySelector('.stage-grid') as HTMLElement)
    fireEvent.click(within(grid).getByTitle('Details'))
    fireEvent.click(await waitFor(() => screen.getByText('⧉ Duplicate card')))

    await waitFor(() => expect(screen.getByText(/is the name this card already has/)).toBeTruthy())
    expect(onEditCharacter, 'a refused duplicate must not open an editor on nothing').not.toHaveBeenCalled()
  })
})
