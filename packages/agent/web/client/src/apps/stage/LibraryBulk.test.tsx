// @vitest-environment happy-dom
//
// Bulk group edits on the library's character grid.
//
// The arithmetic is unit-tested (bulkMembers/bulkState in platform/groups); what
// this covers is the wiring, where the interesting failure lives: one request
// carrying the whole selection, rather than one request per card — or, worse,
// one request carrying only the last card clicked.
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/preact'
import { fakeClient } from '../../platform/ctrlproto/testing'
import type { Group, Verb } from '../../platform/ctrlproto/types'
import { Library } from './Library'

const CARDS = [
  { id: 'c1', name: 'Ivy', greetings: 1 },
  { id: 'c2', name: 'Rook', greetings: 1 },
  { id: 'c3', name: 'Wren', greetings: 1 },
]

// "Ready" already holds Wren, so the same chip has to mean two things depending
// on what is selected.
const GROUPS: Group[] = [{ id: 'g1', name: 'Ready', color: '#8c8', members: ['c3'] }]

function stub() {
  return fakeClient({
    respond: (method: Verb) => {
      switch (method) {
        case 'cards.list':
          return { cards: CARDS }
        case 'cardgroups.list':
          return { groups: GROUPS }
        case 'personas.list':
          return { personas: [] }
        case 'sessions.list':
          return { sessions: [] }
        case 'cardgroups.set_members':
          return GROUPS[0]
        default:
          return {}
      }
    },
  })
}

function library() {
  const client = stub()
  render(<Library client={client} ready status="open" onOpenChat={() => {}} onEditCharacter={() => {}} onEditYou={() => {}} />)
  return client
}

// The grid's tiles, scoped so "Ivy" cannot also match the chats strip or a sheet.
const tile = (name: string) => within(document.querySelector('.stage-grid') as HTMLElement).getByText(name)
const bar = () => document.querySelector('.stage-bulkbar') as HTMLElement

// The library persists its sort preference at mount. happy-dom supplies no
// localStorage here, so give it a real in-memory one rather than a partial stub
// — a half-implemented Storage fails later and further from the cause.
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

describe('bulk group edits', () => {
  it('sends one request carrying the whole selection, not one per card', async () => {
    const client = library()
    await waitFor(() => expect(tile('Ivy')).toBeTruthy())

    fireEvent.click(screen.getByText('Select'))
    fireEvent.click(tile('Ivy'))
    fireEvent.click(tile('Rook'))
    expect(within(bar()).getByText('2 selected')).toBeTruthy()

    fireEvent.click(within(bar()).getByText('Ready'))
    await waitFor(() => expect(client.send.mock.calls.some((c) => c[0] === 'cardgroups.set_members')).toBe(true))

    const writes = client.send.mock.calls.filter((c) => c[0] === 'cardgroups.set_members')
    expect(writes, 'a bulk add is one write per GROUP, not per card').toHaveLength(1)
    // Wren was already a member and stays; the two picked cards are appended.
    expect(writes[0][1]).toEqual({ id: 'g1', members: ['c3', 'c1', 'c2'] })
  })

  it('removes the selection when the group already holds all of it', async () => {
    const client = library()
    await waitFor(() => expect(tile('Wren')).toBeTruthy())

    fireEvent.click(screen.getByText('Select'))
    fireEvent.click(tile('Wren'))

    // Same chip, opposite meaning — it is the state of the selection that
    // decides, which is the whole reason there is one control and not two.
    fireEvent.click(within(bar()).getByText('Ready'))
    await waitFor(() => expect(client.send.mock.calls.some((c) => c[0] === 'cardgroups.set_members')).toBe(true))
    expect(client.send.mock.calls.find((c) => c[0] === 'cardgroups.set_members')![1]).toEqual({ id: 'g1', members: [] })
  })

  it('picks a card instead of opening it while selecting', async () => {
    const client = library()
    await waitFor(() => expect(tile('Ivy')).toBeTruthy())

    fireEvent.click(screen.getByText('Select'))
    fireEvent.click(tile('Ivy'))
    // Opening a character creates a session; the ordinary click must not fire.
    expect(client.send.mock.calls.some((c) => c[0] === 'sessions.create')).toBe(false)
    expect(within(bar()).getByText('1 selected')).toBeTruthy()
  })

  it('drops the selection on leaving, so the next bulk click starts clean', async () => {
    library()
    await waitFor(() => expect(tile('Ivy')).toBeTruthy())

    fireEvent.click(screen.getByText('Select'))
    fireEvent.click(tile('Ivy'))
    fireEvent.click(screen.getByText('Done'))
    fireEvent.click(screen.getByText('Select'))
    expect(within(bar()).getByText('Pick characters to act on')).toBeTruthy()
  })

  it('selects and clears every visible card from one control', async () => {
    library()
    await waitFor(() => expect(tile('Ivy')).toBeTruthy())

    fireEvent.click(screen.getByText('Select'))
    fireEvent.click(within(bar()).getByText('Select all'))
    expect(within(bar()).getByText('3 selected')).toBeTruthy()
    fireEvent.click(within(bar()).getByText('Select none'))
    expect(within(bar()).getByText('Pick characters to act on')).toBeTruthy()
  })

  it('favourites the whole selection', async () => {
    const client = library()
    await waitFor(() => expect(tile('Ivy')).toBeTruthy())

    fireEvent.click(screen.getByText('Select'))
    fireEvent.click(within(bar()).getByText('Select all'))
    fireEvent.click(within(bar()).getByTitle('Favorite the selected characters'))

    await waitFor(() => expect(client.send.mock.calls.filter((c) => c[0] === 'cards.favorite')).toHaveLength(3))
    expect(client.send.mock.calls.filter((c) => c[0] === 'cards.favorite').map((c) => c[1])).toEqual([
      { id: 'c1', favorite: true },
      { id: 'c2', favorite: true },
      { id: 'c3', favorite: true },
    ])
  })
})
