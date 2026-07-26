// @vitest-environment happy-dom
import { afterEach, describe, expect, it, vi } from 'vitest'
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/preact'
import { fakeClient } from '../../platform/ctrlproto/testing'
import type { ModelInfo, PersonaSummary } from '../../platform/ctrlproto/types'
import { newSessionOpts, PanelLanding } from './PanelLanding'

const personas: PersonaSummary[] = [{ name: 'Mieli', ref: 'mieli', origin: 'built-in', emoji: '🧠', specialty: 'planning' }]
const models: ModelInfo[] = [{ id: 'gpt-5.6', provider: 'openai', auth: 'apikey' }]

function client() {
  return fakeClient({
    respond: (method) => {
      if (method === 'personas.list') return { personas }
      if (method === 'personas.get') return { ...personas[0], charter: 'Plans carefully.' }
      return {}
    },
  })
}

function landing(onNewSession = vi.fn()) {
  const c = client()
  render(
    <PanelLanding
      client={c}
      status="open"
      stageEnabled={false}
      models={models}
      onNewSession={onNewSession}
      sessions={[]}
      current=""
      onSelect={() => {}}
      onRename={() => {}}
      onDelete={() => {}}
    />,
  )
  return onNewSession
}

afterEach(cleanup)

describe('PanelLanding', () => {
  it('shows the new-session hero and the persona roster once personas load', async () => {
    landing()
    expect(screen.getByText('Start a new session')).toBeTruthy()
    await waitFor(() => expect(screen.getByText('Mieli')).toBeTruthy())
  })

  // The roster used to depend on a race. app.tsx passes the client out of a ref
  // populated in its mount effect, so the first render with a client is usually
  // one where the socket is still connecting — Client.send rejects "not
  // connected", the catch empties the roster, and nothing retried because the
  // client identity never changed again. "No personas available." for the life
  // of the tab.
  it('waits for the socket rather than asking a connecting one', async () => {
    const c = client()
    const props = {
      stageEnabled: false,
      models,
      onNewSession: () => {},
      sessions: [],
      current: '',
      onSelect: () => {},
      onRename: () => {},
      onDelete: () => {},
    }
    const { rerender } = render(<PanelLanding client={c} status="connecting" {...props} />)
    expect(screen.getByText('No personas available.')).toBeTruthy()
    expect(c.send.mock.calls.some((call) => call[0] === 'personas.list')).toBe(false)

    rerender(<PanelLanding client={c} status="open" {...props} />)
    await waitFor(() => expect(screen.getByText('Mieli')).toBeTruthy())
  })

  it('opens the new-session sheet from the hero', () => {
    landing()
    fireEvent.click(screen.getByText('Start a new session'))
    expect(screen.getByText('New session')).toBeTruthy()
    expect(screen.getByText('Persona')).toBeTruthy()
    expect(screen.getByText('Model')).toBeTruthy()
  })

  it('starts a session as a persona from its detail sheet', async () => {
    const onNew = landing()
    await waitFor(() => expect(screen.getByText('Mieli')).toBeTruthy())
    fireEvent.click(screen.getByText('Mieli'))
    const start = await screen.findByText('Start a session as Mieli')
    fireEvent.click(start)
    expect(onNew).toHaveBeenCalledWith({ persona: 'mieli' })
  })
})

describe('newSessionOpts', () => {
  it('threads a chosen persona and provider-qualified model', () => {
    expect(newSessionOpts('mieli', models[0])).toEqual({ persona: 'mieli', provider: 'openai', model: 'gpt-5.6' })
  })
  it('omits both when untouched — a bare session on the workspace default', () => {
    expect(newSessionOpts('', undefined)).toEqual({})
  })
  it('keeps persona alone when no model is chosen', () => {
    expect(newSessionOpts('mieli', undefined)).toEqual({ persona: 'mieli' })
  })
})
