// @vitest-environment happy-dom
//
// Creating a character from nothing — which Stage could not do at all: cards only
// ever arrived by file or URL import.
//
// The interesting part is that a card's id is CONTENT-DERIVED (build.cardID is a
// name slug plus a hash of the normalized document), so it does not exist until
// there is content. That makes the first save an import and every save after it
// an edit, and the editor has to notice the difference — a second import would
// mint a second card and leave the first one orphaned in the library.
import { afterEach, describe, expect, it, vi } from 'vitest'
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/preact'
import { fakeClient } from '../../platform/ctrlproto/testing'
import type { Verb } from '../../platform/ctrlproto/types'
import { CardEditor } from './CardEditor'

const MINTED = 'kobeni-a1b2c3d4e5f6'

function stub() {
  return fakeClient({
    respond: (method: Verb) => {
      switch (method) {
        case 'cards.import':
          return { id: MINTED, name: 'Kobeni', raw: { spec: 'chara_card_v2', data: { name: 'Kobeni' } } }
        case 'cards.lint':
          return { findings: [] }
        case 'cards.edit':
          return {}
        default:
          return {}
      }
    },
  })
}

afterEach(cleanup)

// The Name field. Queried structurally rather than by label: EditField wraps its
// input in a <label> whose text also carries the field's Hint, so the accessible
// name is not the word alone and several fields match a loose query. Name is the
// first input of the first row.
function nameField(): HTMLElement {
  const el = document.querySelector('.stage-cardeditor__row input')
  if (!el) throw new Error('the editor has no Name field')
  return el as HTMLElement
}

// Decode what the editor put on the wire: cards.import takes the document as
// base64 bytes, the same shape a downloaded card arrives in.
function importedDoc(bytes: string): { data?: Record<string, unknown> } {
  return JSON.parse(new TextDecoder().decode(Uint8Array.from(atob(bytes), (c) => c.charCodeAt(0))))
}

describe('creating a character', () => {
  it('asks nothing of the store until the first save', async () => {
    const client = stub()
    render(<CardEditor client={client} card={null} onClose={() => {}} onSaved={() => {}} />)
    await waitFor(() => expect(screen.getByText('New character')).toBeTruthy())

    // No id exists, so there is nothing to fetch, lint, or consult about. Asking
    // would be a request against an id the store has never seen.
    expect(client.sent('cards.get').length).toBe(0)
    expect(client.sent('cards.lint').length).toBe(0)
    expect(client.sent('models.default_for').length).toBe(0)

    // And the doctor says why it is unavailable rather than failing when pressed.
    expect(screen.getByText('Save first')).toBeTruthy()
    expect((screen.getByText('Save first') as HTMLButtonElement).disabled).toBe(true)
  })

  it('first save IMPORTS the document and adopts the minted id', async () => {
    const client = stub()
    const onCreated = vi.fn()
    const onSaved = vi.fn()
    render(<CardEditor client={client} card={null} onClose={() => {}} onSaved={onSaved} onCreated={onCreated} />)
    await waitFor(() => expect(screen.getByText('New character')).toBeTruthy())

    fireEvent.input(nameField(), { target: { value: 'Kobeni' } })
    fireEvent.click(screen.getByText('Create character'))

    await waitFor(() => expect(client.sent('cards.import').length).toBe(1))
    const sent = client.sent('cards.import')[0].params as { bytes: string }
    expect(importedDoc(sent.bytes).data?.name).toBe('Kobeni')

    // The id the store minted comes back to the host, which needs it for its
    // route — and the editor keeps it, which is what makes the NEXT save an edit.
    await waitFor(() => expect(onCreated).toHaveBeenCalledWith(MINTED))
    expect(onSaved).toHaveBeenCalled()
    // Now that a card exists, it can be linted.
    await waitFor(() => expect(client.sent('cards.lint').length).toBe(1))
  })

  it('the second save EDITS that card rather than importing another', async () => {
    const client = stub()
    render(<CardEditor client={client} card={null} onClose={() => {}} onSaved={() => {}} />)
    await waitFor(() => expect(screen.getByText('New character')).toBeTruthy())

    fireEvent.input(nameField(), { target: { value: 'Kobeni' } })
    fireEvent.click(screen.getByText('Create character'))
    await waitFor(() => expect(client.sent('cards.import').length).toBe(1))

    // The button stops offering to create once there is something to change.
    const save = await screen.findByText('Save changes')
    fireEvent.click(save)

    await waitFor(() => expect(client.sent('cards.edit').length).toBe(1))
    expect(client.sent('cards.edit')[0].params).toMatchObject({ id: MINTED })
    // Exactly one import, ever. A second would leave the first card orphaned.
    expect(client.sent('cards.import').length).toBe(1)
  })

  it('an existing character is fetched and edited, never imported', async () => {
    const client = stub()
    render(
      <CardEditor client={client} card={{ id: MINTED, name: 'Kobeni', greetings: 1 }} onClose={() => {}} onSaved={() => {}} />,
    )
    await waitFor(() => expect(client.sent('cards.get').length).toBe(1))
    fireEvent.click(await screen.findByText('Save changes'))
    await waitFor(() => expect(client.sent('cards.edit').length).toBe(1))
    expect(client.sent('cards.import').length).toBe(0)
  })
})
