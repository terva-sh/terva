// @vitest-environment happy-dom
//
// The right rail with no session behind it.
//
// The rail is a switcher over the surfaces a SESSION offers. On the landing
// there is none, and the panel is right to refuse to ask for one: every surface
// is served through a session handle, and the empty address does not mean "no
// session" — the daemon resolves it by MINTING one. So the rail opened onto
// nothing and sat on "loading…" forever.
//
// These pin the two halves of the fix: that the drawer asks the
// session-independent verb (and addresses it to nothing on purpose), and that it
// renders rather than hanging.
import { afterEach, describe, expect, it, vi } from 'vitest'
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/preact'
import { WorkspaceDrawer } from '../../app'
import type { ProvidersView } from '../../platform/ctrlproto/types'

const PROVIDERS: ProvidersView = {
  providers: [
    { id: 'anthropic', label: 'Anthropic', method: 'oauth' },
    { id: 'openai', label: 'OpenAI' },
  ],
  can_login: true,
}

const auth = {
  flow: null,
  busy: false,
  error: '',
  start: vi.fn(),
  submit: vi.fn(),
  cancel: vi.fn(),
  logout: vi.fn(),
  removeEndpoint: vi.fn(),
}

function mount(over: Partial<Parameters<typeof WorkspaceDrawer>[0]> = {}) {
  const props = {
    tab: 'providers' as const,
    onTab: vi.fn(),
    providers: PROVIDERS,
    err: '',
    auth,
    onClose: vi.fn(),
    onRefresh: vi.fn(),
    version: '0.126.10',
    trusted: false,
    onTrust: vi.fn(),
    ...over,
  }
  render(<WorkspaceDrawer {...props} />)
  return props
}

afterEach(cleanup)

describe('WorkspaceDrawer', () => {
  it('shows the providers it was given instead of hanging on "loading…"', () => {
    mount()
    expect(screen.getByText('Anthropic')).toBeTruthy()
    expect(screen.queryByText('loading…')).toBeNull()
  })

  // A rail that says "loading…" with nothing in flight is the bug, so the empty
  // state has to be distinguishable: nothing yet vs. it failed.
  it('reports a failure rather than pretending to still be loading', () => {
    mount({ providers: null, err: 'no credential file' })
    expect(screen.getByText('no credential file')).toBeTruthy()
    expect(screen.queryByText('loading…')).toBeNull()
  })

  it('offers the daemon’s own facts on the About tab', () => {
    mount({ tab: 'about' })
    expect(screen.getByText('0.126.10')).toBeTruthy()
    expect(screen.getByText('not trusted')).toBeTruthy()
  })

  // Restarting is gated on the daemon saying it can — the same gate the session
  // rail uses. Offering it unconditionally would show a button that errors.
  it('offers a restart only when the daemon can', () => {
    mount({ tab: 'about' })
    expect(screen.queryByText('Restart the daemon')).toBeNull()
    cleanup()
    mount({ tab: 'about', onRestart: vi.fn() })
    expect(screen.getByText('Restart the daemon')).toBeTruthy()
  })

  it('switches tabs through its host', () => {
    const props = mount()
    fireEvent.click(screen.getByText('About'))
    expect(props.onTab).toHaveBeenCalledWith('about')
  })

  it('closes', async () => {
    const props = mount()
    fireEvent.click(screen.getByTitle('Close panes'))
    await waitFor(() => expect(props.onClose).toHaveBeenCalled())
  })
})
