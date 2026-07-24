// @vitest-environment happy-dom
//
// The contract on CardEditor.onSaved: "a save landed — refresh your copy", never
// "the user is finished".
//
// It fires on EVERY save, and one of those saves is not the user leaving: "Save &
// ask again" persists the staged edits first, so the doctor re-reads the improved
// card, and then consults again. A caller that closed the editor from onSaved
// unmounted it in the middle of that round trip and took the proposals, the
// accept/decline verdicts and the typed decline reasons with it. In a session
// that looked exactly like the page reloading.
//
// The Library got this right (onSaved={load}) and the Chat got it wrong, which is
// why it only happened when you opened a character from inside a session.
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
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

// A client that answers the editor's fetches and hands back one proposal from
// every doctor pass, so the consultation loop has something to show.
function stub() {
  return fakeClient({
    respond: (method: Verb) => {
      switch (method) {
        case 'cards.get':
          return { id: 'c1', name: 'Kobeni', raw: { spec: 'chara_card_v2', data: { name: 'Kobeni', personality: 'cold' } } }
        case 'cards.lint':
          return { findings: [] }
        case 'cards.doctor':
          return { proposals: [PROPOSAL], note: '' }
        case 'cards.edit':
          return {}
        default:
          return {}
      }
    },
  })
}

afterEach(cleanup)

async function openWithProposals(onClose: () => void, onSaved: () => void) {
  const client = stub()
  render(<CardEditor client={client} card={CARD} onClose={onClose} onSaved={onSaved} />)
  // Wait for the card fetch to populate the form before touching the doctor.
  await waitFor(() => expect(client.sent('cards.get').length).toBe(1))
  fireEvent.click(await screen.findByText('Ask the doctor'))
  await waitFor(() => expect(client.sent('cards.doctor').length).toBe(1))
  return client
}

describe('CardEditor.onSaved', () => {
  it('"Save & ask again" saves, consults again, and does NOT close the editor', async () => {
    const onClose = vi.fn()
    const onSaved = vi.fn()
    const client = await openWithProposals(onClose, onSaved)

    fireEvent.click(await screen.findByText('Save & ask again'))

    // The save happened...
    await waitFor(() => expect(client.sent('cards.edit').length).toBe(1))
    // ...the host was told to refresh...
    expect(onSaved).toHaveBeenCalled()
    // ...the consultation continued (a SECOND doctor pass)...
    await waitFor(() => expect(client.sent('cards.doctor').length).toBe(2))
    // ...and the editor was never asked to close. This is the whole bug: a
    // caller closing on onSaved unmounted the editor between the save and the
    // second consult.
    expect(onClose).not.toHaveBeenCalled()
    // Still mounted and still showing the loop.
    expect(screen.getByText('Save & ask again')).toBeTruthy()
  })

  it('a plain save keeps the editor open too', async () => {
    const onClose = vi.fn()
    const onSaved = vi.fn()
    const client = await openWithProposals(onClose, onSaved)

    fireEvent.click(await screen.findByText('Save changes'))

    await waitFor(() => expect(client.sent('cards.edit').length).toBe(1))
    expect(onSaved).toHaveBeenCalled()
    expect(onClose).not.toHaveBeenCalled()
  })

  // The decline reason is the part that costs the user real typing, and it only
  // reaches the doctor on the NEXT pass — so it is exactly what an unmount
  // between the save and that pass destroys.
  it('carries a typed decline reason into the next pass', async () => {
    const onClose = vi.fn()
    const client = await openWithProposals(onClose, vi.fn())

    fireEvent.click(await screen.findByText('Decline'))
    const box = await screen.findByPlaceholderText('Why? (e.g. keep the backstory) — the doctor weighs this next pass')
    fireEvent.input(box, { target: { value: 'she is meant to be cold' } })
    fireEvent.click(await screen.findByText('Confirm decline'))

    fireEvent.click(await screen.findByText('Save & ask again'))
    await waitFor(() => expect(client.sent('cards.doctor').length).toBe(2))

    const second = client.sent('cards.doctor')[1].params as { decisions?: { reason?: string }[] }
    expect(second.decisions?.[0]?.reason).toBe('she is meant to be cold')
    expect(onClose).not.toHaveBeenCalled()
  })
})

// The component test above pins the EDITOR's behaviour, but the bug that shipped
// was at the CALL SITE: Chat passed an onSaved that closed the editor, and no
// test could see it because every test supplies its own onSaved. So the rule is
// also asserted where callers live — the way boundaries.test.ts asserts layering.
describe('no CardEditor caller closes on save', () => {
  // There is one caller now — the studio — which is itself the fix: the editor
  // has a single lifecycle instead of one per host. The rule stays because the
  // shape of the bug (a host wiring its own teardown into onSaved) is available
  // to any future caller, and this is the only place it can be caught.
  const CALLERS = ['CharacterStudio.tsx']

  // element returns each <CardEditor …/> usage in a file, source and all.
  function elements(src: string): string[] {
    const out: string[] = []
    let at = src.indexOf('<CardEditor')
    while (at !== -1) {
      const end = src.indexOf('/>', at)
      if (end === -1) break
      out.push(src.slice(at, end))
      at = src.indexOf('<CardEditor', end)
    }
    return out
  }

  // prop returns the source of one JSX prop's value, to the end of its line or
  // its balanced arrow body — enough to see which setters it calls.
  function prop(el: string, name: string): string {
    const at = el.indexOf(`${name}={`)
    if (at === -1) return ''
    let depth = 0
    for (let i = at + name.length + 1; i < el.length; i++) {
      if (el[i] === '{') depth++
      else if (el[i] === '}') {
        depth--
        if (depth === 0) return el.slice(at, i + 1)
      }
    }
    return el.slice(at)
  }

  for (const file of CALLERS) {
    it(`${file} refreshes on save without closing`, () => {
      const els = elements(readFileSync(resolve(__dirname, file), 'utf8'))
      expect(els.length, `no <CardEditor> found in ${file}`).toBeGreaterThan(0)
      for (const el of els) {
        const onClose = prop(el, 'onClose')
        const onSaved = prop(el, 'onSaved')
        expect(onSaved, `${file}: <CardEditor> has no onSaved`).not.toBe('')
        // Whatever tears the editor down in onClose must not appear in onSaved.
        // That is exactly the shape of the bug: setCardEditing(false) in both,
        // so a save was indistinguishable from the user leaving. Identifiers
        // rather than setters specifically, so the rule still bites now that the
        // teardown is a handed-in callback (onBack) instead of a local setter.
        const teardown = (onClose.match(/[A-Za-z_][A-Za-z0-9_]*/g) ?? []).filter(
          (id) => !['onClose', 'props', 'setState'].includes(id),
        )
        expect(teardown.length, `${file}: could not read onClose's teardown`).toBeGreaterThan(0)
        for (const id of teardown) {
          expect(onSaved.includes(id), `${file}: onSaved references ${id}, the same teardown onClose uses — a save would unmount the editor mid-consultation`).toBe(false)
        }
      }
    })
  }
})
