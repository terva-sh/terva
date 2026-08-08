// @vitest-environment happy-dom
//
// Deleting a World from the shelf.
//
// This is a REGRESSION test before it is a feature test. Delete used to live on
// the Library's World drawer; when the drawer became a full screen (WS-2), the
// button moved to the bottom of that screen and nothing replaced it here. The
// operation still existed and the shelf no longer said so, which is the worst
// of the three possible states — a user cleaning up test Worlds concluded there
// was no way and emptied them by hand instead.
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/preact'
import { fakeClient } from '../../platform/ctrlproto/testing'
import type { Verb } from '../../platform/ctrlproto/types'
import { Library } from './Library'

const WORLDS = [
  { id: 'bellhaven-1', name: 'Bellhaven', characters: { Kobeni: 'kobeni-1' } },
  { id: 'lowtown-2', name: 'Lowtown', characters: {} },
]

function mount() {
  const sent: { method: string; params: Record<string, unknown> }[] = []
  let worlds = [...WORLDS]
  const client = fakeClient({
    respond: (method: Verb, params: unknown) => {
      sent.push({ method, params: (params ?? {}) as Record<string, unknown> })
      switch (method) {
        case 'cards.list':
          return { cards: [] }
        case 'personas.list':
          return { personas: [] }
        case 'sessions.list':
          return { sessions: [] }
        case 'worlds.list':
          return { worlds }
        case 'worlds.delete':
          worlds = worlds.filter((w) => w.id !== (params as { id: string }).id)
          return {}
        default:
          return {}
      }
    },
  })
  const view = render(
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
  return { ...view, sent }
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

describe('deleting a World from the shelf', () => {
  it('sends worlds.delete for the World whose ✕ was clicked', async () => {
    vi.stubGlobal('confirm', () => true)
    const { sent } = mount()
    await screen.findByText('Bellhaven')

    // By accessible name, not by position: two ✕ buttons that differ only in
    // their order in the DOM is exactly the bug where the wrong World is
    // deleted, and an index-based query would not notice.
    fireEvent.click(screen.getByRole('button', { name: /Delete the World “Lowtown”/ }))

    await waitFor(() => {
      const del = sent.filter((s) => s.method === 'worlds.delete')
      expect(del).toHaveLength(1)
      expect(del[0].params).toMatchObject({ id: 'lowtown-2' })
    })
    // ...and the shelf re-reads, so the deleted World actually leaves.
    await waitFor(() => expect(screen.queryByText('Lowtown')).toBeNull())
    expect(screen.getByText('Bellhaven')).toBeTruthy()
  })

  it('deletes nothing when the confirm is declined', async () => {
    vi.stubGlobal('confirm', () => false)
    const { sent } = mount()
    await screen.findByText('Bellhaven')

    fireEvent.click(screen.getByRole('button', { name: /Delete the World “Bellhaven”/ }))
    expect(sent.some((s) => s.method === 'worlds.delete')).toBe(false)
    expect(screen.getByText('Bellhaven')).toBeTruthy()
  })

  // The ✕ sits beside the tile, and the tile navigates. If a click on the ✕
  // also reached the tile the author would land in the World they just asked
  // to delete — which is why these are siblings rather than nested.
  it('does not open the World it is deleting', async () => {
    vi.stubGlobal('confirm', () => true)
    const opened: string[] = []
    const client = fakeClient({
      respond: (method: Verb) => {
        switch (method) {
          case 'cards.list':
            return { cards: [] }
          case 'personas.list':
            return { personas: [] }
          case 'sessions.list':
            return { sessions: [] }
          case 'worlds.list':
            return { worlds: WORLDS }
          default:
            return {}
        }
      },
    })
    render(
      <Library
        client={client}
        ready
        status="open"
        onOpenChat={() => {}}
        onEditCharacter={() => {}}
        onEditYou={() => {}}
        onOpenWorld={(id: string) => opened.push(id)}
      />,
    )
    await screen.findByText('Bellhaven')
    fireEvent.click(screen.getByRole('button', { name: /Delete the World “Bellhaven”/ }))
    expect(opened).toEqual([])
  })
})
