// @vitest-environment happy-dom
//
// Steering the doctor, and the third kind of proposal.
//
// The doctor could already read a card and answer its lint, and it could already
// negotiate — accept, decline with a reason, revise. What it could not do was
// take direction ("make her wearier, cut the war backstory") or propose a
// DELETION: an empty `after` is indistinguishable from a model with nothing to
// say, so it was dropped, and the author's own intent had nowhere to go at all.
import { afterEach, describe, expect, it, vi } from 'vitest'
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/preact'
import { fakeClient } from '../../platform/ctrlproto/testing'
import type { Verb } from '../../platform/ctrlproto/types'
import { CardEditor } from './CardEditor'

const CARD = { id: 'c1', name: 'Kobeni', greetings: 1 }
const STEER_BOX = 'e.g. make her wearier, and cut the war backstory'

// A removal: `after` is empty and `remove` is what makes that mean something.
const REMOVAL = {
  id: 'r1',
  field: 'system_prompt',
  severity: 'suggestion',
  rationale: 'the card does not need an override',
  before: 'Answer in verse.',
  after: '',
  remove: true,
}

function stub(proposals: unknown[] = []) {
  return fakeClient({
    respond: (method: Verb) => {
      switch (method) {
        case 'cards.get':
          return {
            id: 'c1',
            name: 'Kobeni',
            raw: { spec: 'chara_card_v2', spec_version: '2.0', data: { name: 'Kobeni', system_prompt: 'Answer in verse.' } },
          }
        case 'cards.lint':
          return { findings: [] }
        case 'cards.doctor':
          return { proposals, note: '' }
        default:
          return {}
      }
    },
  })
}

async function mount(proposals: unknown[] = []) {
  const client = stub(proposals)
  render(<CardEditor client={client} card={CARD} onClose={vi.fn()} onSaved={vi.fn()} />)
  await waitFor(() => expect(client.sent('cards.get').length).toBe(1))
  return client
}

afterEach(cleanup)

describe('the doctor takes direction', () => {
  it('sends what the author asked for with the consultation', async () => {
    const client = await mount()

    fireEvent.input(await screen.findByPlaceholderText(STEER_BOX), { target: { value: 'make her wearier' } })
    fireEvent.click(await screen.findByText('Ask the doctor'))

    await waitFor(() => expect(client.sent('cards.doctor').length).toBe(1))
    expect((client.sent('cards.doctor')[0].params as { steer?: string }).steer).toBe('make her wearier')
  })

  // A steer is a standing brief, not a one-shot: the whole negotiation is meant
  // to work toward it. Dropping it after the first pass would quietly revert the
  // doctor to a lint-led read on round two, with nothing on screen to say so.
  it('keeps steering on the revise round', async () => {
    const client = await mount([REMOVAL])

    fireEvent.input(await screen.findByPlaceholderText(STEER_BOX), { target: { value: 'cut the war backstory' } })
    fireEvent.click(await screen.findByText('Ask the doctor'))
    await waitFor(() => expect(client.sent('cards.doctor').length).toBe(1))

    fireEvent.click(await screen.findByText('Save & ask again'))
    await waitFor(() => expect(client.sent('cards.doctor').length).toBe(2))
    expect((client.sent('cards.doctor')[1].params as { steer?: string }).steer).toBe('cut the war backstory')
  })

  // Editing it between rounds is how an author changes course — the second pass
  // must carry the NEW brief, not the one the consultation opened with.
  it('carries an edited brief into the next round', async () => {
    const client = await mount([REMOVAL])

    const box = await screen.findByPlaceholderText(STEER_BOX)
    fireEvent.input(box, { target: { value: 'make her wearier' } })
    fireEvent.click(await screen.findByText('Ask the doctor'))
    await waitFor(() => expect(client.sent('cards.doctor').length).toBe(1))

    fireEvent.input(box, { target: { value: 'actually, warmer' } })
    fireEvent.click(await screen.findByText('Save & ask again'))
    await waitFor(() => expect(client.sent('cards.doctor').length).toBe(2))
    expect((client.sent('cards.doctor')[1].params as { steer?: string }).steer).toBe('actually, warmer')
  })
})

describe('a proposal that removes', () => {
  // `after` is empty by design, so the proposal has to SAY it deletes something.
  // Rendered as-is it is a blank panel under "Proposed", which reads as a broken
  // suggestion — the one shape a user would refuse for the wrong reason.
  it('reads as a deletion rather than an empty box', async () => {
    await mount([REMOVAL])
    fireEvent.click(await screen.findByText('Ask the doctor'))

    expect(await screen.findByText('Proposed — remove')).toBeTruthy()
    expect(screen.getByText('(this field is cleared)')).toBeTruthy()
    // The current value is still shown, so what is being lost is visible.
    expect(screen.getByText('Answer in verse.')).toBeTruthy()
  })

  // ...and it has to actually remove: applying stages the empty value, and the
  // save writes it into the document that reaches cards.edit.
  it('clears the field when applied and saved', async () => {
    const client = await mount([REMOVAL])
    fireEvent.click(await screen.findByText('Ask the doctor'))

    fireEvent.click(await screen.findByText('Apply'))
    fireEvent.click(await screen.findByText('Save changes'))

    await waitFor(() => expect(client.sent('cards.edit').length).toBe(1))
    const doc = (client.sent('cards.edit')[0].params as { card: { data: Record<string, unknown> } }).card
    expect(doc.data.system_prompt).toBe('')
    // Only the proposed field goes: a removal is not a reset.
    expect(doc.data.name).toBe('Kobeni')
  })
})
