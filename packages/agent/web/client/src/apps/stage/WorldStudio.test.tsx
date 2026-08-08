// @vitest-environment happy-dom
//
// The World studio. The property under test throughout is that NO SESSION is
// involved: every read and write here goes through a `worlds.*` verb carrying the
// World's id, because the whole point of the screen is reviewing and editing a
// World you are not currently playing. A regression that reintroduced a session
// dependency would still render — it would just fail for anyone on the shelf.
import { afterEach, describe, expect, it, vi } from 'vitest'
import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/preact'
import { fakeClient } from '../../platform/ctrlproto/testing'
import type { Verb, WorldView } from '../../platform/ctrlproto/types'
import { WorldStudio } from './WorldStudio'

const WORLD: WorldView = {
  id: 'bellhaven-1',
  name: 'Bellhaven',
  description: 'A harbour town that keeps its bargains.',
  characters: { Kobeni: 'kobeni-1' },
  character_models: { Kobeni: { provider: 'openai', model: 'gpt-5' } },
  lore: [
    { name: 'The curfew', keys: ['curfew', 'bells'], content: 'The bells ring at dusk.' },
    { name: 'The compact', constant: true, content: 'Nobody in Bellhaven breaks a deal.', audience: ['Kobeni'] },
  ],
  coordination: '',
}

const MODELS = [
  { id: 'gpt-5', provider: 'openai' },
  { id: 'gpt-5.5', provider: 'openai' },
]

// Records every verb the screen sends, so a test can assert on the WHOLE
// conversation rather than only on what rendered.
function stub(overrides: Partial<Record<string, unknown>> = {}) {
  const sent: { method: string; params: Record<string, unknown>; sess?: string }[] = []
  const client = fakeClient({
    respond: (method: Verb, params: unknown, sess?: string) => {
      sent.push({ method, params: (params ?? {}) as Record<string, unknown>, sess })
      if (method in overrides) return overrides[method as string]
      switch (method) {
        case 'worlds.list':
          return { worlds: [WORLD] }
        case 'cards.list':
          return { cards: [{ id: 'kobeni-1', name: 'Kobeni', greetings: 1, avatar_url: '/media/cards/kobeni-1' }, { id: 'aki-2', name: 'Aki', greetings: 1 }] }
        case 'sessions.list':
          return { sessions: [{ id: 's1', title: 'The first night', world: 'bellhaven-1', messages: 12 }, { id: 's2', title: 'Elsewhere', world: 'other' }] }
        case 'models.list':
          return { models: MODELS }
        // The ladder's own resolver. Keyed on params because the two callers ask
        // different questions: the World's row asks with neither card nor world
        // (it wants the rung BELOW itself), a roster row asks with both.
        case 'models.default_for': {
          const q = (params ?? {}) as { card?: string; world?: string }
          return q.world
            ? { provider: 'openai', model: 'gpt-5.5', source: 'world' }
            : { provider: 'openai', model: 'gpt-5', source: 'workspace' }
        }
        default:
          return WORLD
      }
    },
  })
  return { client, sent }
}

function mount(client: ReturnType<typeof stub>['client'], props: Record<string, unknown> = {}) {
  return render(
    <WorldStudio
      client={client}
      ready
      id="bellhaven-1"
      tab="roster"
      onTab={() => {}}
      backLabel="Library"
      onBack={() => {}}
      onOpenChat={() => {}}
      onEditCharacter={() => {}}
      {...props}
    />,
  )
}

afterEach(cleanup)

describe('the World studio', () => {
  it('opens a World from the shelf with no session', async () => {
    const { client, sent } = stub()
    mount(client)
    await screen.findByRole('heading', { name: /Bellhaven/ })
    // Every opening read carries the World id and NO session.
    const reads = sent.filter((s) => s.method === 'worlds.list')
    expect(reads).toHaveLength(1)
    expect(sent.every((s) => !s.sess)).toBe(true)
    expect(screen.getByText('A harbour town that keeps its bargains.')).toBeTruthy()
  })

  it('says so when the World has been deleted out from under it', async () => {
    const { client } = stub({ 'worlds.list': { worlds: [] } })
    mount(client)
    await screen.findByText('This World is no longer in your library.')
  })

  it('adds a cast member through worlds.add_character', async () => {
    const { client, sent } = stub()
    mount(client)
    await screen.findByRole('heading', { name: /Bellhaven/ })

    // Picking a card pre-fills the roster name but leaves it editable — a World
    // can cast the same card under a different name.
    fireEvent.change(screen.getByRole('combobox', { name: '' }) ?? screen.getAllByRole('combobox')[0], { target: { value: 'aki-2' } })
    const nameBox = screen.getByPlaceholderText('Their name in this World') as HTMLInputElement
    await waitFor(() => expect(nameBox.value).toBe('Aki'))

    fireEvent.click(screen.getByText('Add'))
    await waitFor(() => {
      const add = sent.find((s) => s.method === 'worlds.add_character')
      expect(add).toBeTruthy()
      expect(add?.params).toMatchObject({ id: 'bellhaven-1', name: 'Aki', ref: 'aki-2' })
      expect(add?.sess).toBeFalsy()
    })
  })

  it('drops a cast member only after a confirm, and says the card survives', async () => {
    const { client, sent } = stub()
    const confirm = vi.spyOn(window, 'confirm').mockReturnValue(false)
    mount(client)
    await screen.findByRole('heading', { name: /Bellhaven/ })

    fireEvent.click(screen.getByTitle('Take Kobeni out of this World'))
    expect(sent.some((s) => s.method === 'worlds.remove_character')).toBe(false)
    // The reassurance is the point of the wording: a World is a cast list, not
    // a card owner, and removing someone from it must not read as a delete.
    expect(confirm.mock.calls[0][0]).toContain('card stays in your library')

    confirm.mockReturnValue(true)
    fireEvent.click(screen.getByTitle('Take Kobeni out of this World'))
    await waitFor(() => {
      const rm = sent.find((s) => s.method === 'worlds.remove_character')
      expect(rm?.params).toMatchObject({ id: 'bellhaven-1', name: 'Kobeni' })
    })
    confirm.mockRestore()
  })

  it('marks the cast member this World has its own version of', async () => {
    const { client } = stub({
      'cards.list': {
        cards: [{ id: 'kobeni-1', name: 'Kobeni', greetings: 1, variant_of: 'bellhaven-1' }],
      },
    })
    mount(client)
    // The author has to be able to tell which copy they are editing — an
    // unmarked fork looks exactly like the shared card it was made to protect.
    await screen.findByText('this World’s own version')
  })

  it('does not mark a card that is another World’s variant', async () => {
    const { client } = stub({
      'cards.list': {
        cards: [{ id: 'kobeni-1', name: 'Kobeni', greetings: 1, variant_of: 'somewhere-else' }],
      },
    })
    mount(client)
    await screen.findByRole('heading', { name: /Bellhaven/ })
    expect(screen.queryByText('this World’s own version')).toBeNull()
  })

  it('flags a roster entry whose card has left the library', async () => {
    const { client } = stub({ 'cards.list': { cards: [] } })
    mount(client)
    await screen.findByText('card missing from your library')
  })

  it('sets coordination on the World, not on a session', async () => {
    const { client, sent } = stub()
    mount(client)
    await screen.findByRole('heading', { name: /Bellhaven/ })

    const coord = screen.getByText('Who answers a turn').parentElement!.querySelector('select')!
    fireEvent.change(coord, { target: { value: 'focus:Kobeni' } })
    await waitFor(() => {
      const set = sent.find((s) => s.method === 'worlds.set')
      expect(set?.params).toMatchObject({ id: 'bellhaven-1', coordination: 'focus:Kobeni' })
      expect(set?.sess).toBeFalsy()
    })
  })

  // The World's own default model — the middle rung of card → world →
  // workspace, which existed in the resolver as a reserved no-op until the
  // World got a screen to set it from.
  it('sets the World default model through worlds.set_model', async () => {
    const { client, sent } = stub()
    mount(client)
    await screen.findByRole('heading', { name: /Bellhaven/ })

    // The World's own row asks with NEITHER card nor world: it wants the rung
    // BELOW itself. Asking with the world id would hand back the pin it is
    // already displaying, and the Default row would offer to inherit from the
    // very setting it is offering to clear.
    await waitFor(() => {
      const asks = sent.filter((s) => s.method === 'models.default_for')
      expect(asks.some((a) => !a.params.card && !a.params.world)).toBe(true)
      // ...and the doctor's rung is asked separately: a sessionless run resolves
      // the World's own default, which is neither the row above (the rung below
      // the World) nor a character's.
      expect(asks.some((a) => !a.params.card && a.params.world === 'bellhaven-1')).toBe(true)
    })

    const pick = screen.getByText('Model for this World').parentElement!
    fireEvent.click(within(pick).getByRole('button'))
    fireEvent.click(await within(pick).findByText('gpt-5.5'))
    await waitFor(() => {
      const set = sent.find((s) => s.method === 'worlds.set_model')
      expect(set?.params).toMatchObject({ id: 'bellhaven-1', provider: 'openai', model: 'gpt-5.5' })
      expect(set?.sess).toBeFalsy()
    })
  })

  // Clearing has to be reachable, or a World can be aimed but never un-aimed.
  // The Default row sends two empty strings, which is what the daemon reads as
  // "inherit again" — a row that sent the current values would look identical
  // and quietly pin the World to whatever it happened to be showing.
  it('clears the World default back to the workspace model', async () => {
    const { client, sent } = stub({
      'worlds.list': { worlds: [{ ...WORLD, model: { provider: 'openai', model: 'gpt-5.5' } }] },
    })
    mount(client)
    await screen.findByRole('heading', { name: /Bellhaven/ })

    const pick = screen.getByText('Model for this World').parentElement!
    fireEvent.click(within(pick).getByRole('button'))
    fireEvent.click(await within(pick).findByText('Workspace default'))
    await waitFor(() => {
      const set = sent.find((s) => s.method === 'worlds.set_model')
      expect(set?.params).toMatchObject({ id: 'bellhaven-1', provider: '', model: '' })
    })
  })

  // "Inherit" that will not say what it inherits is the least useful thing a
  // settings row can print, and it got worse the moment inheritance grew a
  // third rung. The label is resolved by the daemon's own authority, so this
  // screen can never disagree with the session it starts.
  it('names the model an unpinned character actually inherits', async () => {
    const { client, sent } = stub({
      'worlds.list': { worlds: [{ ...WORLD, character_models: {} }] },
    })
    mount(client)
    await screen.findByRole('heading', { name: /Bellhaven/ })

    // Asked with the character's card AND the World — the question is what THIS
    // character in THIS World falls back to, not what the workspace default is.
    await waitFor(() => {
      const ask = sent.find((s) => s.method === 'models.default_for' && s.params.card === 'kobeni-1')
      expect(ask?.params).toMatchObject({ card: 'kobeni-1', world: 'bellhaven-1' })
    })
    // Scoped to the roster list — the name also appears in the add-a-character
    // picker and the coordination select, and a bare match would happily assert
    // against either.
    const roster = document.querySelector('.stage-worldroster') as HTMLElement
    const row = within(roster).getByText('Kobeni').closest('li') as HTMLElement
    await within(row).findByText('gpt-5.5')
  })

  it('lists only the scenes played in this World', async () => {
    const { client } = stub()
    mount(client, { tab: 'scenes' })
    // Scoped to the scenes list: the doctor pane stays MOUNTED (so a
    // consultation survives a tab click) and its picker names the same scenes,
    // so an unscoped query matches twice.
    await screen.findByRole('heading', { name: /Bellhaven/ })
    const list = document.querySelector('.stage-yourchats') as HTMLElement
    expect(within(list).getByText('The first night')).toBeTruthy()
    expect(within(list).queryByText('Elsewhere')).toBeNull()
  })

  it('keeps the doctor mounted while another tab is showing', async () => {
    const { client } = stub()
    mount(client, { tab: 'roster' })
    await screen.findByRole('heading', { name: /Bellhaven/ })
    // Present in the DOM but hidden — unmounting would throw away a doctor
    // consultation mid-negotiation on a click that only promised to look at the
    // cast.
    const doctorPane = screen.getByText('🩺 Doctor this world').closest('.stage-studio__pane') as HTMLElement
    expect(doctorPane.hasAttribute('hidden')).toBe(true)
  })
})

describe('the World lorebook', () => {
  it('edits an entry in place, sending the old name as replace', async () => {
    const { client, sent } = stub()
    mount(client, { tab: 'lore' })
    await screen.findByText('The curfew')

    fireEvent.click(screen.getByText('The curfew'))
    const name = screen.getByPlaceholderText('The curfew bells') as HTMLInputElement
    await waitFor(() => expect(name.value).toBe('The curfew'))
    fireEvent.input(name, { target: { value: 'The curfew bells' } })
    fireEvent.click(screen.getByText('Save entry'))

    await waitFor(() => {
      const put = sent.find((s) => s.method === 'worlds.lore.put')
      expect(put?.params).toMatchObject({ id: 'bellhaven-1' })
      // Without `replace` carrying the name the entry was OPENED under, a
      // rename would upsert by the NEW name and stand a second entry beside
      // the first instead of editing it.
      expect(put?.params.replace).toBe('The curfew')
      expect((put?.params.entry as Record<string, unknown>).name).toBe('The curfew bells')
    })
  })

  it('sends no replace for a new entry, so it does not overwrite one', async () => {
    const { client, sent } = stub()
    mount(client, { tab: 'lore' })
    await screen.findByText('The curfew')

    fireEvent.click(screen.getByText('+ New entry'))
    fireEvent.input(screen.getByPlaceholderText('The curfew bells'), { target: { value: 'The harbour' } })
    fireEvent.input(screen.getByPlaceholderText('curfew, bells, dusk'), { target: { value: 'harbour, docks' } })
    fireEvent.input(screen.getByLabelText('Content') ?? screen.getByRole('textbox', { name: 'Content' }), { target: { value: 'Tar and rope.' } })
    fireEvent.click(screen.getByText('Save entry'))

    await waitFor(() => {
      const put = sent.find((s) => s.method === 'worlds.lore.put')
      expect(put?.params.replace).toBeUndefined()
      expect((put?.params.entry as Record<string, unknown>).keys).toEqual(['harbour', 'docks'])
    })
  })

  it('will not save an entry that could never activate', async () => {
    const { client } = stub()
    mount(client, { tab: 'lore' })
    await screen.findByText('The curfew')

    fireEvent.click(screen.getByText('+ New entry'))
    fireEvent.input(screen.getByPlaceholderText('The curfew bells'), { target: { value: 'Unfireable' } })
    // Fill CONTENT first. Checking `disabled` before this proves nothing about
    // the activation rule — an empty entry is refused for being empty, and the
    // keys/always-on clause is never reached. (It caught exactly that: a
    // mutation dropping the clause left this test green.)
    fireEvent.input(screen.getByLabelText('Content'), { target: { value: 'Some state.' } })
    await waitFor(() => expect((screen.getByLabelText('Content') as HTMLTextAreaElement).value).toBe('Some state.'))

    // Named and written, but with no keywords and not always-on the engine
    // would never fire it — so the button refuses rather than the server
    // refusing after a round trip.
    expect((screen.getByText('Save entry') as HTMLButtonElement).disabled).toBe(true)

    // The other half: marking it always-on is what makes it fireable, so the
    // same entry must now be saveable. Without this, "always disabled" would
    // pass too.
    fireEvent.click(screen.getByLabelText('Always in play (no keywords needed)'))
    await waitFor(() => expect((screen.getByText('Save entry') as HTMLButtonElement).disabled).toBe(false))
  })

  it('shows what an entry is scoped to without opening it', async () => {
    const { client } = stub()
    mount(client, { tab: 'lore' })
    const compact = (await screen.findByText('The compact')).closest('li')!
    // An always-on entry says so; a scoped one names its audience. Both are
    // decisions you make about a WORLD, so they have to be legible in the list.
    expect(within(compact).getByText('always on')).toBeTruthy()
    expect(within(compact).getByText('known to Kobeni')).toBeTruthy()
  })

  it('deletes an entry through worlds.lore.delete after a confirm', async () => {
    const { client, sent } = stub()
    const confirm = vi.spyOn(window, 'confirm').mockReturnValue(true)
    mount(client, { tab: 'lore' })
    await screen.findByText('The curfew')

    fireEvent.click(screen.getByTitle('Delete “The curfew”'))
    await waitFor(() => {
      const del = sent.find((s) => s.method === 'worlds.lore.delete')
      expect(del?.params).toMatchObject({ id: 'bellhaven-1', name: 'The curfew' })
      expect(del?.sess).toBeFalsy()
    })
    confirm.mockRestore()
  })
})
