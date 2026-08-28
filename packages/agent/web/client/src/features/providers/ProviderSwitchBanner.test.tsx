// @vitest-environment happy-dom
//
// The provider-switch banner in the Providers pane.
//
// The daemon cannot stop and ask a browser the way the TUI asks a terminal, so
// on a lapsed pin it starts on whatever works and reports it here. Everything
// below is about that report being actionable rather than merely present: the
// pane already showed the lapsed subscription as an "expired" row, and that row
// never said which account the next turn would actually bill.
import { afterEach, describe, expect, it, vi } from 'vitest'
import { cleanup, fireEvent, render, screen } from '@testing-library/preact'
import { ProvidersBody } from '../../app'
import type { ProvidersView } from '../../platform/ctrlproto/types'

afterEach(cleanup)

const BASE: ProvidersView = {
  providers: [
    { id: 'anthropic', label: 'Anthropic', method: 'oauth', expired: true },
    { id: 'openai', label: 'OpenAI', method: 'apikey' },
  ],
  can_login: true,
}

function withSwitch(over: Partial<NonNullable<ProvidersView['switch']>> = {}): ProvidersView {
  return {
    ...BASE,
    switch: {
      from: 'anthropic',
      from_model: 'claude-opus-5',
      to: 'openai',
      to_model: 'gpt-5',
      reason: 'anthropic login expired and could not be refreshed',
      lapsed: true,
      ...over,
    },
  }
}

function mount(v: ProvidersView, onUseProvider?: (p: string, m: string) => void) {
  return render(
    <ProvidersBody
      v={v}
      flow={null}
      onStart={vi.fn()}
      onSubmit={vi.fn()}
      onCancel={vi.fn()}
      onLogout={vi.fn()}
      onRemoveEndpoint={vi.fn()}
      onUseProvider={onUseProvider}
      busy={false}
      error=""
    />,
  )
}

describe('provider switch banner', () => {
  it('names both the lapsed pin and what is running instead', () => {
    mount(withSwitch())
    // The whole point: BOTH halves. "anthropic expired" alone leaves the reader
    // believing nothing is running; "on openai" alone looks like their own
    // choice.
    expect(document.body.textContent).toContain('anthropic')
    expect(document.body.textContent).toContain('openai/gpt-5')
  })

  it('says the pin is still the default', () => {
    mount(withSwitch())
    // Otherwise the banner reads as "your default changed", and the user goes
    // looking for a setting to change back that was never changed.
    expect(document.body.textContent).toContain('claude-opus-5')
    expect(document.body.textContent).toMatch(/still your default/i)
  })

  it('offers the switch as an action carrying the exact pair', () => {
    const onUse = vi.fn()
    mount(withSwitch(), onUse)
    fireEvent.click(screen.getByText(/Start a chat on openai\/gpt-5/i))
    expect(onUse).toHaveBeenCalledWith('openai', 'gpt-5')
  })

  it('renders nothing when the pin is fine', () => {
    mount(BASE)
    expect(document.body.textContent).not.toMatch(/still your default/i)
    expect(screen.queryByText(/Start a chat on/i)).toBeNull()
  })

  // A host that cannot open a session must not show a button that does nothing —
  // the same rule can_login already applies to the login controls.
  it('reports without the action when no handler is wired', () => {
    mount(withSwitch())
    expect(document.body.textContent).toMatch(/still your default/i)
    expect(screen.queryByText(/Start a chat on/i)).toBeNull()
  })

  // A pin that was never logged in has no account to renew. Both cases are worth
  // reporting; only one of them makes "sign in again" the remedy.
  it('does not call a never-configured pin expired', () => {
    mount(withSwitch({ lapsed: false, reason: 'no credential for anthropic' }))
    expect(document.body.textContent).not.toMatch(/login expired/i)
    expect(document.body.textContent).toMatch(/no usable credential/i)
  })

  // to_model is omitempty on the wire. "openai/" reads as a bug; "openai" reads
  // as a provider, which is all we know when the model is absent.
  it('degrades to the provider alone when the model is absent', () => {
    mount(withSwitch({ to_model: undefined }), vi.fn())
    expect(document.body.textContent).not.toContain('openai/')
    expect(screen.getByText(/Start a chat on openai/i)).toBeTruthy()
  })
})
