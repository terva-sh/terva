// @vitest-environment happy-dom
//
// A doctor pass that suggests nothing is a RESTING POINT, not a dead end.
//
// "No changes suggested — the card looks good." used to end the consultation
// for good. The head's "Ask the doctor" is gone the moment `proposals` is set
// (it gates on `!proposals`, and an empty array is not null), and the "Save &
// ask again" button lived inside `proposals.length > 0`. So the steer box and
// the model picker sat there, editable, with nothing to act on them: the only
// way to a second opinion — or the same opinion from a stronger model — was to
// close the editor and reopen it.
import { afterEach, describe, expect, it, vi } from 'vitest'
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/preact'
import { fakeClient } from '../../platform/ctrlproto/testing'
import type { Verb } from '../../platform/ctrlproto/types'
import { CardEditor } from './CardEditor'

const CARD = { id: 'c1', name: 'Kobeni', greetings: 1 }

const PROPOSAL = {
  id: 'p1',
  field: 'personality',
  severity: 'suggestion',
  rationale: 'reads cold',
  before: 'cold',
  after: 'warm but wary',
}

// `passes` is what each successive cards.doctor call answers, so a test can say
// "this round is terminal" and then ask what the editor still offers.
function stub(passes: { proposals: unknown[]; note?: string }[]) {
  let n = 0
  return fakeClient({
    respond: (method: Verb) => {
      switch (method) {
        case 'cards.get':
          return { id: 'c1', name: 'Kobeni', raw: { spec: 'chara_card_v2', data: { name: 'Kobeni', personality: 'cold' } } }
        case 'cards.lint':
          return { findings: [] }
        case 'cards.doctor':
          return passes[Math.min(n++, passes.length - 1)]
        default:
          return {}
      }
    },
  })
}

async function consult(passes: { proposals: unknown[]; note?: string }[]) {
  const client = stub(passes)
  render(<CardEditor client={client} card={CARD} onClose={() => {}} onSaved={() => {}} />)
  await waitFor(() => expect(client.sent('cards.get').length).toBe(1))
  fireEvent.click(await screen.findByText('Ask the doctor'))
  await waitFor(() => expect(client.sent('cards.doctor').length).toBe(1))
  return client
}

afterEach(cleanup)

describe('a terminal doctor pass', () => {
  it('still offers another round when the first pass suggests nothing', async () => {
    const client = await consult([{ proposals: [] }])
    expect(await screen.findByText('No changes suggested — the card looks good.')).toBeTruthy()

    // The whole bug: this button did not exist here.
    fireEvent.click(await screen.findByText('Save & ask again'))
    await waitFor(() => expect(client.sent('cards.doctor').length).toBe(2))
  })

  it('still offers another round after a round of proposals ends in one', async () => {
    // The likelier route in: you work through proposals, hit "Save & ask
    // again", and the next pass declares the card good.
    const client = await consult([{ proposals: [PROPOSAL] }, { proposals: [] }])
    fireEvent.click(await screen.findByText('Save & ask again'))
    await waitFor(() => expect(client.sent('cards.doctor').length).toBe(2))

    expect(await screen.findByText('No changes suggested — the card looks good.')).toBeTruthy()
    fireEvent.click(await screen.findByText('Save & ask again'))
    await waitFor(() => expect(client.sent('cards.doctor').length).toBe(3))
  })

  it('carries what you changed after the verdict into the next round', async () => {
    // The other half of the report: re-steering, or choosing a different model,
    // was possible all along — running on it was not. Both ride the same params,
    // and the steer box is the half happy-dom can drive.
    const client = await consult([{ proposals: [] }])
    fireEvent.input(screen.getByPlaceholderText(/make her wearier/i), { target: { value: 'try a harsher read' } })
    fireEvent.click(await screen.findByText('Save & ask again'))

    await waitFor(() => expect(client.sent('cards.doctor').length).toBe(2))
    const second = (client.sent('cards.doctor')[1] as { params: Record<string, unknown> }).params
    expect(second.steer).toBe('try a harsher read')
    // The model override travels alongside it, empty here = the card's default.
    expect(second).toHaveProperty('provider')
    expect(second).toHaveProperty('model')
  })

  it('saves before consulting, so a hand-edit after the verdict is read', async () => {
    // The doctor reads the STORED card. Re-asking without saving would re-read
    // the same bytes and reach the same verdict — this bug wearing a hat.
    const client = await consult([{ proposals: [] }])
    fireEvent.input(screen.getByLabelText(/Personality/i, { selector: 'textarea' }), { target: { value: 'warm but wary' } })
    fireEvent.click(await screen.findByText('Save & ask again'))

    await waitFor(() => expect(client.sent('cards.doctor').length).toBe(2))
    expect(client.sent('cards.edit').length, 'the edit must land before the second pass reads the card').toBe(1)
  })

  it('shows no proposal list and no "Apply all" when there is nothing to apply', async () => {
    await consult([{ proposals: [] }])
    await screen.findByText('Save & ask again')
    expect(document.querySelector('.stage-doctor__list')).toBeNull()
    expect(screen.queryByText('Apply all')).toBeNull()
  })

  it('does not bring back the first-run button, which would consult unsaved', async () => {
    // "Ask the doctor" skips the save. Offering both here would be two buttons
    // that differ only in whether your typing survives.
    await consult([{ proposals: [] }])
    await screen.findByText('Save & ask again')
    expect(screen.queryByText('Ask the doctor')).toBeNull()
  })
})

// A guard for the order of operations the fix depends on, kept honest against
// the source rather than the render: save() is awaited BEFORE cards.doctor.
describe('reviseDoctor', () => {
  it('refuses to consult on a stale card when the save fails', async () => {
    const client = fakeClient({
      respond: (method: Verb) => {
        if (method === 'cards.get') return { id: 'c1', name: 'Kobeni', raw: { spec: 'chara_card_v2', data: { name: 'Kobeni' } } }
        if (method === 'cards.lint') return { findings: [] }
        if (method === 'cards.doctor') return { proposals: [] }
        if (method === 'cards.edit') throw new Error('disk full')
        return {}
      },
    })
    render(<CardEditor client={client} card={CARD} onClose={() => {}} onSaved={vi.fn()} />)
    await waitFor(() => expect(client.sent('cards.get').length).toBe(1))
    fireEvent.click(await screen.findByText('Ask the doctor'))
    await waitFor(() => expect(client.sent('cards.doctor').length).toBe(1))

    fireEvent.click(await screen.findByText('Save & ask again'))
    await waitFor(() => expect(client.sent('cards.edit').length).toBe(1))
    // No second pass: consulting on a card the daemon never accepted would put
    // the doctor's verdict and the editor's contents out of step.
    expect(client.sent('cards.doctor').length).toBe(1)
  })
})
