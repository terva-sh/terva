// @vitest-environment happy-dom
import { afterEach, describe, expect, it } from 'vitest'
import { fakeClient } from '../../platform/ctrlproto/testing'
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/preact'
import type { ModelInfo } from '../../platform/ctrlproto/types'
import { ModelPick } from './ModelPick'

// "Session model" names the routing, not the model. These pin that every surface
// which offers an inherit option also says WHAT is inherited — the picker is
// where you decide whether to override, and that decision cannot be made against
// a label that only repeats the question.
const models: ModelInfo[] = [
  { id: 'gpt-5.6-sol', provider: 'openai-codex', auth: 'oauth', context_window: 400_000 },
  { id: 'gpt-5.6-sol', provider: 'openai', auth: 'apikey' },
  { id: 'glm-5.2', provider: 'zai', auth: 'apikey', default: true },
]

// A client stub that answers models.list and nothing else. ModelPick loads the
// catalog lazily, so a collapsed picker never reaches it.
function stubClient(list: ModelInfo[] = models) {
  return fakeClient({ respond: () => ({ models: list }) })
}

afterEach(cleanup)

describe('ModelPick inherit row', () => {
  it('names the inherited model on the collapsed button, before the catalog loads', () => {
    const client = stubClient()
    render(
      <ModelPick
        client={client}
        sessionId="s1"
        onSelect={() => {}}
        defaultLabel="Session model"
        defaultProvider="openai-codex"
        defaultModel="gpt-5.6-sol"
      />,
    )
    expect(screen.getByText('Session model')).toBeTruthy()
    // The point of the whole change: the button answers "which model?" without
    // being opened, and without a models.list round trip.
    const qual = screen.getByText('gpt-5.6-sol')
    expect(qual.getAttribute('title')).toBe('Session model: gpt-5.6-sol (openai-codex)')
    expect(client.send).not.toHaveBeenCalled()
  })

  it('qualifies the inherit row with provider and billing once the list is open', async () => {
    render(
      <ModelPick
        client={stubClient()}
        sessionId="s1"
        onSelect={() => {}}
        defaultLabel="Session model"
        defaultProvider="openai"
        defaultModel="gpt-5.6-sol"
      />,
    )
    fireEvent.click(screen.getByTitle('Pick a model for this run'))
    // The same model id is reachable through two providers with different
    // billing; resolving against the catalog is what lets the row say which.
    await waitFor(() => expect(screen.getByText('gpt-5.6-sol · openai')).toBeTruthy())
    // Not getByText('Session model') — the label is on the collapsed button too.
    const row = screen.getByText('gpt-5.6-sol · openai').closest('.stage-modelpick__row')
    expect(row?.querySelector('.stage-modelpick__auth--oauth')).toBeNull()
    expect(row?.querySelector('.stage-modelpick__auth')?.textContent).toBe('api')
  })

  it('falls back to the workspace default when no session model is given', async () => {
    // The card doctor's picker has no session to ask, so its "Workspace model"
    // resolves from the catalog's own default entry instead.
    render(<ModelPick client={stubClient()} sessionId="" onSelect={() => {}} defaultLabel="Workspace model" />)
    fireEvent.click(screen.getByTitle('Pick a model for this run'))
    await waitFor(() => expect(screen.getByText('glm-5.2 · zai')).toBeTruthy())
  })

  it('shows the override, not the inherited model, once one is picked', () => {
    render(
      <ModelPick
        client={stubClient()}
        sessionId="s1"
        currentProvider="zai"
        currentModel="glm-5.2"
        onSelect={() => {}}
        defaultLabel="Session model"
        defaultProvider="openai-codex"
        defaultModel="gpt-5.6-sol"
      />,
    )
    expect(screen.getByText('glm-5.2')).toBeTruthy()
    expect(screen.getByText('zai')).toBeTruthy()
    // The inherited model must not linger next to an active override — two model
    // ids on one button reads as "which of these is running?".
    expect(screen.queryByText('gpt-5.6-sol')).toBeNull()
  })
})
