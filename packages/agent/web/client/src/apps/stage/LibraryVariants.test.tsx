// @vitest-environment happy-dom
//
// World-scoped variants are off the Library shelf.
//
// A variant is a fork worlds.edit_character made so an edit inside one World
// would not rewrite the character every other World is playing. On the shelf it
// would read as a near-duplicate of a character you already have, with no way to
// tell which is which — so it is hidden, and SAID to be hidden, because a fork
// the author accepted is a card they may go looking for.
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { cleanup, render, screen, within } from '@testing-library/preact'
import { fakeClient } from '../../platform/ctrlproto/testing'
import type { Verb } from '../../platform/ctrlproto/types'
import { Library } from './Library'

const CARDS = [
  { id: 'kobeni-1', name: 'Kobeni', greetings: 1 },
  { id: 'aki-2', name: 'Aki', greetings: 1 },
  // The same character, as one World rewrote her. A variant carries BOTH fields.
  { id: 'kobeni-9', name: 'Kobeni', greetings: 1, variant_of: 'bellhaven-1', world_of: 'bellhaven-1' },
  // A character the World INVENTED — world_of, no variant_of.
  { id: 'kira-3', name: 'Kira', greetings: 1, world_of: 'bellhaven-1' },
]

function mount(cards: unknown[]) {
  const client = fakeClient({
    respond: (method: Verb) => {
      switch (method) {
        case 'cards.list':
          return { cards }
        case 'personas.list':
          return { personas: [] }
        case 'sessions.list':
          return { sessions: [] }
        case 'worlds.list':
          return { worlds: [{ id: 'bellhaven-1', name: 'Bellhaven' }] }
        default:
          return {}
      }
    },
  })
  return render(
    <Library
      client={client}
      ready
      status="open"
      onOpenChat={() => {}}
      onEditCharacter={() => {}}
      onEditYou={() => {}}
      onOpenWorld={() => {}}
    />,
  )
}

// The library persists its sort preference at mount, and happy-dom supplies no
// localStorage here — a real in-memory one, matching LibraryBulk's, because a
// half-implemented Storage fails later and further from the cause.
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

describe('World variants on the shelf', () => {
  it('keeps a variant off the grid but says it exists', async () => {
    mount(CARDS)
    await screen.findByText('Aki')
    // Two Kobenis in the library, one on the shelf.
    expect(screen.getAllByText('Kobeni')).toHaveLength(1)
    // Hiding it silently would read as a lost edit, so the count is stated.
    expect(screen.getByText('1 character is a World’s own version — open that World to see it.')).toBeTruthy()
  })

  it('says nothing when there are no variants', async () => {
    mount(CARDS.slice(0, 2))
    await screen.findByText('Aki')
    expect(screen.queryByText(/World’s own version/)).toBeNull()
  })

  // A character a World created is NOT a variant. A fork is hidden because its
  // original is still on the shelf; this one has no original, so hiding it would
  // put the card beyond reach entirely — unfindable, unexportable, unreusable.
  it('keeps a World-born character ON the shelf', async () => {
    mount(CARDS)
    await screen.findByText('Aki')
    expect(screen.getByText('Kira')).toBeTruthy()
  })

  // ...and says which World, so the shelf is legible at a glance.
  it('badges it with the World that made it', async () => {
    mount(CARDS)
    const kira = (await screen.findByText('Kira')).closest('.stage-card') as HTMLElement
    expect(within(kira).getByText(/Bellhaven/)).toBeTruthy()
  })

  // The badge must NOT be the card's name. card.name is the {{char}} macro and
  // an input to the content-addressed id, so folding the World into it would
  // change what the model calls her mid-scene.
  it('leaves the card name alone', async () => {
    mount(CARDS)
    const name = await screen.findByText('Kira')
    expect(name.textContent).toBe('Kira')
    expect(name.className).toContain('stage-card__name')
  })

  // An ordinary card gets no badge — the marking has to mean something.
  it('does not badge a card that belongs to no World', async () => {
    mount(CARDS)
    const aki = (await screen.findByText('Aki')).closest('.stage-card') as HTMLElement
    expect(within(aki).queryByText(/Bellhaven/)).toBeNull()
  })
})
