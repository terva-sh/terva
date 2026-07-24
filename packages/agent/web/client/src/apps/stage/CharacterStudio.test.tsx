// @vitest-environment happy-dom
//
// The studio's two tabs are the two halves of a story — who you play WITH, and
// who you play AS. The lifecycle rule is the load-bearing part: a tab reads as a
// lighter act than leaving, so switching must not tear down what the other pane
// was in the middle of. Unmounting it would throw away an unsaved card draft, or
// a doctor consultation mid-negotiation, on a click that promised only to look
// elsewhere — the same shape as the save-closes-the-editor bug this screen was
// built to make unreachable.
import { useState } from 'preact/hooks'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/preact'
import { fakeClient } from '../../platform/ctrlproto/testing'
import type { Verb } from '../../platform/ctrlproto/types'
import { CharacterStudio, type StudioTab } from './CharacterStudio'

const CARD = { id: 'c1', name: 'Kobeni', greetings: 1 }

function stub() {
  return fakeClient({
    respond: (method: Verb) => {
      switch (method) {
        case 'cards.get':
          return { id: 'c1', name: 'Kobeni', raw: { spec: 'chara_card_v2', data: { name: 'Kobeni', personality: 'cold' } } }
        case 'cards.lint':
          return { findings: [] }
        case 'userpersonas.list':
          return { personas: [{ ref: 'kira', name: 'Kira' }] }
        default:
          return {}
      }
    },
  })
}

// The tab lives in the host (Stage), so the test supplies the same wiring rather
// than a stubbed no-op — a tab that never changes cannot show a remount.
function Host(props: { start: StudioTab }) {
  const [tab, setTab] = useState<StudioTab>(props.start)
  return (
    <CharacterStudio
      client={stub()}
      ready
      card={CARD}
      tab={tab}
      onTab={setTab}
      scene={null}
      backLabel="Library"
      onBack={() => {}}
    />
  )
}

afterEach(cleanup)

// Both panes are in the document at once, so a bare getByText would find the
// tab AND the pane heading it names. Scope every tab click to the strip.
const tab = (label: string) => within(document.querySelector('.stage-studio__tabs') as HTMLElement).getByText(label)

describe('CharacterStudio tabs', () => {
  it('keeps an unsaved card draft alive across a trip to the You tab', async () => {
    render(<Host start="character" />)
    const personality = await screen.findByDisplayValue('cold')
    fireEvent.input(personality, { target: { value: 'warm but wary' } })

    fireEvent.click(tab('You'))
    await waitFor(() => expect(screen.getByText('Your personas')).toBeTruthy())

    fireEvent.click(tab('Kobeni'))
    // The same edit, still there — the pane was hidden, not unmounted.
    expect(await screen.findByDisplayValue('warm but wary')).toBeTruthy()
  })

  // The hidden pane must not merely be transparent: [hidden] is defeated by the
  // pane's own `display: flex`, so this pins the attribute the CSS rule keys on.
  it('hides the pane it is not on', async () => {
    render(<Host start="you" />)
    const panes = document.querySelectorAll('.stage-studio__pane')
    expect(panes).toHaveLength(2)
    expect(panes[0].hasAttribute('hidden')).toBe(true)
    expect(panes[1].hasAttribute('hidden')).toBe(false)

    fireEvent.click(tab('Kobeni'))
    await waitFor(() => expect(document.querySelectorAll('.stage-studio__pane')[0].hasAttribute('hidden')).toBe(false))
    expect(document.querySelectorAll('.stage-studio__pane')[1].hasAttribute('hidden')).toBe(true)
  })

  // The character's tab is named after the character: "them and you" is the
  // whole point of the pairing, and "Character | You" says half of it.
  it('names the character tab after the character', () => {
    render(<Host start="character" />)
    expect(tab('Kobeni')).toBeTruthy()

    cleanup()
    const client = stub()
    render(
      <CharacterStudio client={client} ready card={null} tab="character" onTab={vi.fn()} scene={null} backLabel="Library" onBack={() => {}} />,
    )
    expect(tab('New character')).toBeTruthy()
  })
})
