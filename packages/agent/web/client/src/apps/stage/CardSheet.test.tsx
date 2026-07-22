// @vitest-environment happy-dom
import { afterEach, describe, expect, it } from 'vitest'
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/preact'
import { fakeClient } from '../../platform/ctrlproto/testing'
import type { DefaultForResult, ModelInfo, Verb } from '../../platform/ctrlproto/types'
import { CardSheet } from './CardSheet'

const MODELS: ModelInfo[] = [
  { id: 'gpt-5.5', provider: 'openai', auth: 'apikey' },
  { id: 'glm-5.2', provider: 'zai', auth: 'apikey' },
]

// A client that answers the three fetches CardSheet fires on open, plus the
// picker's models.list. `defaultFor` drives the card's resolved default (or an
// Error to simulate an old daemon without the verb).
function stub(defaultFor: DefaultForResult | Error) {
  return fakeClient({
    respond: (method: Verb) => {
      switch (method) {
        case 'cards.get':
          return { id: 'c1', name: 'Kobeni', raw: { data: {} } }
        case 'cards.lint':
          return { findings: [] }
        case 'models.list':
          return { models: MODELS }
        case 'models.default_for':
          if (defaultFor instanceof Error) throw defaultFor
          return defaultFor
        case 'cardmodel.set':
          return {}
        default:
          return {}
      }
    },
  })
}

const card = { id: 'c1', name: 'Kobeni', greetings: 1 }

afterEach(cleanup)

const SELECT_TITLE = 'Pick a model for this run'

describe('CardSheet default model row', () => {
  it('shows the card’s own default as the active pick', async () => {
    const client = stub({ provider: 'openai', model: 'gpt-5.5', source: 'card' })
    render(<CardSheet client={client} card={card} busy={false} onClose={() => {}} onStart={() => {}} />)
    // source==='card' → the picker names the card's model, not the inherit label.
    await waitFor(() => expect(screen.getByText('gpt-5.5')).toBeTruthy())
    expect(screen.getByText('Default model')).toBeTruthy()
  })

  it('shows the workspace fallback label when the card inherits', async () => {
    const client = stub({ provider: 'openai', model: 'gpt-5.5', source: 'workspace' })
    render(<CardSheet client={client} card={card} busy={false} onClose={() => {}} onStart={() => {}} />)
    // source!=='card' → no card-specific pick, so the picker shows what it inherits.
    await waitFor(() => expect(screen.getByText('Workspace default')).toBeTruthy())
  })

  it('files a card default when a model is picked', async () => {
    const client = stub({ provider: 'openai', model: 'gpt-5.5', source: 'workspace' })
    render(<CardSheet client={client} card={card} busy={false} onClose={() => {}} onStart={() => {}} />)
    await waitFor(() => expect(screen.getByText('Workspace default')).toBeTruthy())
    fireEvent.click(screen.getByTitle(SELECT_TITLE)) // open the list
    await waitFor(() => expect(screen.getByText('glm-5.2')).toBeTruthy())
    fireEvent.click(screen.getByText('glm-5.2').closest('.stage-modelpick__row')!)
    const cmd = client.last('cardmodel.set')
    expect(cmd?.params).toEqual({ card: 'c1', provider: 'zai', model: 'glm-5.2' })
  })

  it('clears the card default from the picker’s Default row', async () => {
    const client = stub({ provider: 'openai', model: 'gpt-5.5', source: 'card' })
    render(<CardSheet client={client} card={card} busy={false} onClose={() => {}} onStart={() => {}} />)
    await waitFor(() => expect(screen.getByText('gpt-5.5')).toBeTruthy())
    fireEvent.click(screen.getByTitle(SELECT_TITLE)) // open the list
    // The inherit row (defaultLabel) clears the pref — empty strings on the wire.
    await waitFor(() => expect(screen.getByText('Workspace default')).toBeTruthy())
    fireEvent.click(screen.getByText('Workspace default').closest('.stage-modelpick__row')!)
    expect(client.last('cardmodel.set')?.params).toEqual({ card: 'c1', provider: '', model: '' })
  })

  it('hides the row on a daemon that does not serve the resolver', async () => {
    const client = stub(new Error('unsupported'))
    render(<CardSheet client={client} card={card} busy={false} onClose={() => {}} onStart={() => {}} />)
    // Wait until the (failing) resolve has been attempted, then assert it left no row.
    await waitFor(() => expect(client.sent('models.default_for').length).toBe(1))
    await waitFor(() => expect(screen.getByText('Kobeni')).toBeTruthy()) // sheet still rendered
    expect(screen.queryByText('Default model')).toBeNull()
  })
})
