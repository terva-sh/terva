// @vitest-environment happy-dom
//
// The world doctor's accept surfaces. Three things can go wrong here that no
// server test can see: a proposal applied through the wrong verb, a decision
// that does not ride back into the next round, and a card edit written into the
// wrong level of the CCv2 document (which fails SILENTLY — cards.edit re-parses,
// so a top-level write on a v2 wrapper is dropped and the panel looks like it
// worked).
import { afterEach, describe, expect, it } from 'vitest'
import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/preact'
import { fakeClient } from '../../platform/ctrlproto/testing'
import type { Verb, WorldCardProposal } from '../../platform/ctrlproto/types'
import { WorldDoctor, groupByCharacter } from './WorldDoctor'
import { applyCardFields } from './cardraw'

const RESULT = {
  note: 'the cast has no authority figure',
  card_proposals: [
    {
      id: 'c1',
      card: 'kobeni-1',
      character: 'Kobeni',
      field: 'personality',
      severity: 'suggestion',
      rationale: 'nothing to play against Aki',
      before: 'anxious',
      after: 'anxious, but sharper when cornered',
    },
  ],
  world_proposals: [
    { id: 'w1', kind: 'lore_entry', rationale: 'delta', name: 'The Contract', content: 'Hunters sign in blood.', keys: ['contract'] },
    { id: 'w2', kind: 'lore_retire', rationale: 'outgrown', name: 'The Bureau', content: 'Public Safety runs the hunts.' },
    { id: 'w3', kind: 'character_new', rationale: 'no authority', character: 'Makima', description: 'the one who gives the orders' },
  ],
}

function stub(result: unknown = RESULT) {
  return fakeClient({
    respond: (method: Verb) => {
      switch (method) {
        case 'worlds.doctor':
          return result
        case 'cards.get':
          return { id: 'kobeni-1', name: 'Kobeni', raw: { spec: 'chara_card_v2', data: { name: 'Kobeni', personality: 'anxious' } } }
        case 'cards.import':
          return { id: 'makima-9', name: 'Makima' }
        case 'worlds.edit_character':
          return { world: WORLD, card_id: 'kobeni-fork', forked: true }
        case 'worlds.lore.put':
        case 'worlds.lore.delete':
        case 'worlds.add_character':
          return WORLD
        case 'models.list':
          return { models: [] }
        default:
          return {}
      }
    },
  })
}

const WORLD = {
  id: 'w-1',
  name: 'Tokyo Division',
  characters: { Kobeni: 'kobeni-1' },
}

const SCENES = [
  { id: 's1', title: 'The first night', world: 'w-1' },
  { id: 's2', title: 'The harbour job', world: 'w-1' },
  { id: 's3', title: 'The long quiet', world: 'w-1' },
  { id: 's4', title: 'An older night', world: 'w-1' },
]

// One mount helper: the panel is World-scoped now, so every case needs the same
// four props and none of them needs a session.
function mount(client: ReturnType<typeof stub>, scenes = SCENES, onWorld: (w: unknown) => void = () => {}) {
  return render(<WorldDoctor client={client} world={WORLD as never} scenes={scenes as never} onWorld={onWorld as never} />)
}

async function runDoctor(result: { note: string } & Record<string, unknown> = RESULT) {
  const client = stub(result)
  mount(client)
  fireEvent.click(screen.getByText('🩺 Doctor this world'))
  // Wait on THIS result's note rather than a hardcoded one, so a test that
  // supplies its own payload does not silently wait for someone else's.
  await waitFor(() => expect(screen.getByText(result.note)).toBeTruthy())
  return client
}

// The row a named proposal renders in — addressing by content rather than by
// index, because applying a proposal removes its own buttons and every later
// index shifts under the test.
const rowFor = (title: string) => screen.getByText(title).closest('li') as HTMLElement

afterEach(cleanup)

describe('the scene picker', () => {
  // A World's evidence is its whole history of play, and which nights matter is
  // a judgement — so the scenes are OFFERED rather than assumed. Pre-checking a
  // few keeps a long history from costing a full read by default.
  it('pre-checks the most recent few and sends exactly those', async () => {
    const client = stub()
    mount(client)
    fireEvent.click(screen.getByText('🩺 Doctor this world'))
    await waitFor(() => expect(client.sent('worlds.doctor').length).toBe(1))
    const p = client.last('worlds.doctor')?.params as { id?: string; sessions?: string[] }
    expect(p.id).toBe('w-1')
    expect(p.sessions).toEqual(['s1', 's2', 's3'])
  })

  it('sends the scenes you actually picked', async () => {
    const client = stub()
    mount(client)
    // Drop the first, add the fourth.
    fireEvent.click(screen.getByLabelText('The first night'))
    fireEvent.click(screen.getByLabelText('An older night'))
    fireEvent.click(screen.getByText('🩺 Doctor this world'))
    await waitFor(() => expect(client.sent('worlds.doctor').length).toBe(1))
    expect((client.last('worlds.doctor')?.params as { sessions?: string[] }).sessions).toEqual(['s2', 's3', 's4'])
  })

  // The studio loads its sessions ASYNCHRONOUSLY, so the doctor's first render
  // has no scenes at all. A default seeded once at mount would therefore be
  // empty forever, and every box would render unchecked while the panel claimed
  // to be reading the recent scenes. Only a re-render with scenes arriving late
  // shows it — which is exactly how it shipped broken and was caught in a
  // screenshot rather than here.
  it('pre-checks scenes that arrive after the first render', async () => {
    const client = stub()
    const { rerender } = render(<WorldDoctor client={client} world={WORLD as never} scenes={[] as never} onWorld={() => {}} />)
    rerender(<WorldDoctor client={client} world={WORLD as never} scenes={SCENES as never} onWorld={() => {}} />)
    expect((screen.getByLabelText('The first night') as HTMLInputElement).checked).toBe(true)

    fireEvent.click(screen.getByText('🩺 Doctor this world'))
    await waitFor(() => expect(client.sent('worlds.doctor').length).toBe(1))
    expect((client.last('worlds.doctor')?.params as { sessions?: string[] }).sessions).toEqual(['s1', 's2', 's3'])
  })

  // Unchecking ONE box must not discard the other two: the first interaction has
  // to materialize the derived default rather than start from nothing.
  it('keeps the other defaults when one is unchecked', async () => {
    const client = stub()
    mount(client)
    fireEvent.click(screen.getByLabelText('The first night'))
    fireEvent.click(screen.getByText('🩺 Doctor this world'))
    await waitFor(() => expect(client.sent('worlds.doctor').length).toBe(1))
    expect((client.last('worlds.doctor')?.params as { sessions?: string[] }).sessions).toEqual(['s2', 's3'])
  })

  it('runs with no scenes at all — an unplayed World is the case this was asked for', async () => {
    const client = stub()
    mount(client, [])
    fireEvent.click(screen.getByText('🩺 Doctor this world'))
    await waitFor(() => expect(client.sent('worlds.doctor').length).toBe(1))
    expect((client.last('worlds.doctor')?.params as { sessions?: string[] }).sessions).toEqual([])
  })

  // No session anywhere. That is the property the whole retooling was for: the
  // studio is a Library screen, and a doctor that needed an open scene could not
  // be reached from it.
  it('carries no session on any call', async () => {
    const client = await runDoctor()
    expect(client.sent('worlds.doctor').every((c) => !c.sess)).toBe(true)
  })
})

describe('running', () => {
  it('sends the steer, and re-sends it on a revise', async () => {
    const client = stub()
    mount(client)
    fireEvent.input(document.querySelector('.stage-doctor__steerbox textarea') as HTMLTextAreaElement, {
      target: { value: 'give her a rival' },
    })
    fireEvent.click(screen.getByText('🩺 Doctor this world'))
    await waitFor(() => expect(client.sent('worlds.doctor').length).toBe(1))
    expect((client.last('worlds.doctor')?.params as { steer?: string }).steer).toBe('give her a rival')

    fireEvent.click(screen.getByText('Ask again'))
    await waitFor(() => expect(client.sent('worlds.doctor').length).toBe(2))
    // Standing direction, not a one-time question: a revise that dropped it
    // would silently change the ask between rounds.
    expect((client.last('worlds.doctor')?.params as { steer?: string }).steer).toBe('give her a rival')
  })

  it('renders both families, grouped by character', async () => {
    await runDoctor()
    expect(screen.getByText('The cast')).toBeTruthy()
    expect(screen.getByText('Kobeni')).toBeTruthy()
    expect(screen.getByText('The world')).toBeTruthy()
    expect(screen.getByText('The Contract')).toBeTruthy()
  })

  // An accept REPLACES the field, so the text about to be lost is part of the
  // decision — not just the text arriving.
  it('shows what an accept would replace, not only what it writes', async () => {
    await runDoctor()
    expect(screen.getByText('anxious')).toBeTruthy()
    expect(screen.getByText('anxious, but sharper when cornered')).toBeTruthy()
  })
})

describe('the accepts', () => {
  it('applies a card edit through worlds.edit_character, nested under data', async () => {
    const client = await runDoctor()
    fireEvent.click(screen.getByText('Apply'))
    await waitFor(() => expect(client.sent('worlds.edit_character').length).toBe(1))
    // NOT cards.edit. That is the difference between changing this World's
    // version of a character and changing the character every other World is
    // still playing.
    expect(client.sent('cards.edit').length).toBe(0)

    const params = client.last('worlds.edit_character')?.params as { id: string; character: string; card: Record<string, unknown> }
    // The World is what is addressed; the character names whose card it is. The
    // card ref never appears in the params at all — the server resolves it from
    // the roster, which is what lets a fork move it out from under the client.
    expect(params.id).toBe('w-1')
    expect(params.character).toBe('Kobeni')
    // And the card was READ from the roster's current ref, so a second accept on
    // the same character edits the fork the first one made rather than the
    // original it was made from.
    expect((client.last('cards.get')?.params as { id: string }).id).toBe('kobeni-1')
    // The silent-failure guard: a v2 wrapper carries its fields under `data`,
    // and cards.edit re-parses, so a top-level write is dropped without error.
    expect((params.card.data as Record<string, string>).personality).toBe('anxious, but sharper when cornered')
    expect(params.card.personality).toBeUndefined()
    expect(await screen.findByText('✓ applied')).toBeTruthy()
  })

  it('writes a lore entry through worlds.lore.put', async () => {
    const client = await runDoctor()
    fireEvent.click(screen.getByText('Accept'))
    await waitFor(() => expect(client.sent('worlds.lore.put').length).toBe(1))
    const entry = (client.last('worlds.lore.put')?.params as { entry: { name: string; keys?: string[]; constant?: boolean } }).entry
    expect(entry.name).toBe('The Contract')
    expect(entry.keys).toEqual(['contract'])
    // Keyed, so NOT always-on. An entry that is both would fire every turn.
    expect(entry.constant).toBe(false)
  })

  it('retires through worlds.lore.delete, never through put', async () => {
    const client = await runDoctor()
    fireEvent.click(screen.getByText('Retire it'))
    await waitFor(() => expect(client.sent('worlds.lore.delete').length).toBe(1))
    expect((client.last('worlds.lore.delete')?.params as { name: string }).name).toBe('The Bureau')
    expect(client.sent('worlds.lore.put').length).toBe(0)
  })

  // A new character is imported from the EDITED draft, which is the whole
  // reason the fields are a form and not a preview.
  it('creates a new character from the edited draft in ONE verb', async () => {
    const client = await runDoctor()
    const nameBox = document.querySelectorAll('.stage-doctor__promo textarea')[0] as HTMLTextAreaElement
    fireEvent.input(nameBox, { target: { value: 'Makima, revised' } })
    fireEvent.click(screen.getByText('Add to library & stage'))

    await waitFor(() => expect(client.sent('worlds.create_character').length).toBe(1))
    // The EDITED draft, not the proposal as it arrived — the fields are a form
    // for a reason.
    expect(client.last('worlds.create_character')?.params).toMatchObject({
      id: 'w-1',
      name: 'Makima, revised',
      card: { name: 'Makima, revised' },
    })
    // ONE verb is the property, not an implementation detail. The pair this
    // replaced could half-apply — the import lands, the roster call fails, and
    // the author has a character in their library that no World asked for.
    expect(client.sent('cards.import')).toHaveLength(0)
    expect(client.sent('worlds.add_character')).toHaveLength(0)
  })
})

describe('the negotiation', () => {
  it('rides decisions from BOTH families back into the next round', async () => {
    const client = await runDoctor()
    // Accept the card edit…
    fireEvent.click(screen.getByText('Apply'))
    await waitFor(() => expect(client.sent('worlds.edit_character').length).toBe(1))
    // …and decline a world proposal with a reason.
    const row = rowFor('The Contract')
    fireEvent.click(within(row).getByText('Decline…'))
    fireEvent.input(within(row).getByRole('textbox') as HTMLInputElement, {
      target: { value: 'the contract is already implied' },
    })
    fireEvent.click(within(row).getByText('Decline'))

    fireEvent.click(screen.getByText('Revise with my decisions'))
    await waitFor(() => expect(client.sent('worlds.doctor').length).toBe(2))

    const decisions = (client.last('worlds.doctor')?.params as { decisions: { proposal_id: string; accepted: boolean; reason?: string }[] }).decisions
    // Sending only one family would have the doctor re-propose the other's
    // accepts — the round would go backwards.
    expect(decisions.map((d) => d.proposal_id).sort()).toEqual(['c1', 'w1'])
    expect(decisions.find((d) => d.proposal_id === 'c1')?.accepted).toBe(true)
    expect(decisions.find((d) => d.proposal_id === 'w1')?.reason).toBe('the contract is already implied')
  })

  // The two families name their proposals independently, so an id collision
  // across them is likely rather than theoretical — and a shared verdict map
  // would have one decision silently decide the other.
  it('keeps verdicts apart when both families use the same id', async () => {
    const client = await runDoctor({
      note: 'collision',
      card_proposals: [{ ...RESULT.card_proposals[0], id: 'p1' }],
      world_proposals: [{ id: 'p1', kind: 'lore_entry', rationale: 'x', name: 'An Entry', content: 'body' }],
    })
    fireEvent.click(screen.getByText('Apply'))
    await waitFor(() => expect(client.sent('worlds.edit_character').length).toBe(1))
    // The world proposal sharing the id must still be undecided — its Accept
    // is still on screen.
    expect(screen.getByText('Accept')).toBeTruthy()
  })
})

describe('groupByCharacter', () => {
  const p = (id: string, character: string): WorldCardProposal =>
    ({ id, card: character.toLowerCase(), character, field: 'personality', severity: '', rationale: '', before: '', after: '' })

  it('keeps each character’s proposals together', () => {
    const got = groupByCharacter([p('1', 'Kobeni'), p('2', 'Aki'), p('3', 'Kobeni')])
    expect(got.map(([who, items]) => [who, items.length])).toEqual([
      ['Kobeni', 2],
      ['Aki', 1],
    ])
  })

  // Order follows first mention rather than a sort, so a revise cannot
  // reshuffle the list under someone mid-decision.
  it('orders by first mention, not alphabetically', () => {
    expect(groupByCharacter([p('1', 'Zoe'), p('2', 'Aki')]).map(([who]) => who)).toEqual(['Zoe', 'Aki'])
  })

  it('falls back to the card ref when a proposal has no character name', () => {
    const orphan = { ...p('1', ''), card: 'ref-1' }
    expect(groupByCharacter([orphan]).map(([who]) => who)).toEqual(['ref-1'])
  })
})

describe('applyCardFields', () => {
  it('writes under data for a spec’d v2 document', () => {
    const got = applyCardFields({ spec: 'chara_card_v2', data: { name: 'K', personality: 'old' } }, { personality: 'new' })
    expect((got.data as Record<string, string>).personality).toBe('new')
    expect(got.personality).toBeUndefined()
  })

  // A bare v1 object has no wrapper, so its fields ARE the top level. Writing
  // into a `data` key there would invent one the parser does not read.
  it('writes at the top level for a bare v1 object', () => {
    const got = applyCardFields({ name: 'K', personality: 'old' }, { personality: 'new' })
    expect(got.personality).toBe('new')
    expect(got.data).toBeUndefined()
  })

  it('does not mutate the document it was given', () => {
    const original = { spec: 'chara_card_v2', data: { personality: 'old' } }
    applyCardFields(original, { personality: 'new' })
    expect(original.data.personality).toBe('old')
  })

  it('survives a missing or malformed document', () => {
    expect(applyCardFields(undefined, { personality: 'new' }).personality).toBe('new')
    expect(applyCardFields({ spec: 'chara_card_v2' }, { personality: 'new' }).personality).toBe('new')
  })
})
