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
import type { ProvidersView, SecretsStatus } from '../../platform/ctrlproto/types'

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
  useProvider: vi.fn(),
}

function mountProps(over: Partial<Parameters<typeof WorkspaceDrawer>[0]> = {}) {
  return {
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
}

function mount(over: Partial<Parameters<typeof WorkspaceDrawer>[0]> = {}) {
  const props = mountProps(over)
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

// --- the Secrets tab (--web-allow-secrets) ---
//
// The at-rest posture is served by an OPTIONAL group, off the daemon's base
// hello. A tab that calls a verb the daemon never negotiated is a tab whose
// every control answers "method group not negotiated", so the gate is the
// negotiated group and not a preference.
const SECRETS: SecretsStatus = {
  key: { state: 'present', path: '/home/pat/.terva/secrets.key', mode: '0600', owner_only: true },
  recipient: 'age1exampleexampleexampleexampleexampleexampleexamplexxxxxx',
  files: [{ name: 'auth.json', state: 'encrypted' }],
  store: { present: true, encrypted: true, scopes: [{ scope: 'core:bot.telegram', keys: 1 }] },
  config: { total: 2, plaintext: ['extensions.weather.api_key'], agent_can_read: false, reason: 'a secret is stored in plaintext' },
  reads: [
    { scope: 'conn:matrix', readable: false, enforced: true, reason: 'it has not registered which of its values are secret' },
    { scope: 'ext:memory', readable: false, enforced: false, reason: 'its manifest does not declare "data_secrets"' },
    { scope: 'conn:clean-one', readable: true, enforced: true },
  ],
  grants: [{ principal: 'ext:memory', scope: 'conn:matrix', mode: 'read' }],
}

describe('WorkspaceDrawer — the Secrets tab', () => {
  it('is absent unless the daemon negotiated the group', () => {
    mount({ canReadSecrets: false })
    expect(screen.queryByText('Secrets')).toBeNull()
    cleanup()
    mount({ canReadSecrets: true })
    expect(screen.getByText('Secrets')).toBeTruthy()
  })

  it('reports what is still plaintext, by location', () => {
    mount({ tab: 'secrets', canReadSecrets: true, secrets: SECRETS })
    expect(screen.getByText('extensions.weather.api_key')).toBeTruthy()
    expect(screen.getByText('1 of 2 still plaintext')).toBeTruthy()
  })

  // The pending flip has to read differently from a live denial, or an operator
  // cannot tell "fix this now" from "this is coming".
  it('tells an enforced denial apart from one that is still pending', () => {
    mount({ tab: 'secrets', canReadSecrets: true, secrets: SECRETS })
    expect(screen.getByText(/^denied —/)).toBeTruthy()
    expect(screen.getByText(/will be denied in a future release/)).toBeTruthy()
  })

  // A row per healthy component would bury the ones that need action.
  it('says nothing about a component that is already clean', () => {
    mount({ tab: 'secrets', canReadSecrets: true, secrets: SECRETS })
    expect(screen.queryByText(/conn:clean-one/)).toBeNull()
  })

  // Same empty-state rule the providers half follows: nothing yet vs. it failed.
  it('reports a failure rather than pretending to still be loading', () => {
    mount({ tab: 'secrets', canReadSecrets: true, secrets: null, secretsErr: 'method group not negotiated: secrets' })
    expect(screen.getByText('method group not negotiated: secrets')).toBeTruthy()
    expect(screen.queryByText('loading…')).toBeNull()
  })

  // Found by dumping the rendered pane rather than by a test: an expired grant
  // showed its red text inside a row whose dot said "all fine". A report exists
  // to be scannable, so the summary has to agree with the detail.
  it('marks the grants row when one of them has expired', () => {
    const { container } = render(
      <WorkspaceDrawer
        {...mountProps({
          tab: 'secrets',
          canReadSecrets: true,
          secrets: { ...SECRETS, grants: [{ principal: 'p', scope: 'core:x', mode: 'use', expired: true }] },
        })}
      />,
    )
    const row = Array.from(container.querySelectorAll('.secrets-row')).find((r) => r.textContent?.startsWith('Grants'))
    expect(row?.className).toContain('secrets-row--warn')
  })

  // ...and the other half: a healthy grant list must NOT be flagged, or the
  // assertion above would pass for a row that is always warned.
  it('leaves the grants row alone when nothing has expired', () => {
    const { container } = render(<WorkspaceDrawer {...mountProps({ tab: 'secrets', canReadSecrets: true, secrets: SECRETS })} />)
    const row = Array.from(container.querySelectorAll('.secrets-row')).find((r) => r.textContent?.startsWith('Grants'))
    expect(row?.className).toContain('secrets-row--ok')
  })

  // The whole surface exists on the promise that it carries no material. A
  // renderer that grew a value field would be a leak onto a screen someone else
  // can see, and the daemon's own guarantee would not catch it.
  it('renders no secret value, because none is sent', () => {
    const { container } = render(<WorkspaceDrawer {...mountProps({ tab: 'secrets', canReadSecrets: true, secrets: SECRETS })} />)
    const text = container.textContent ?? ''
    expect(text).toContain('core:bot.telegram')
    for (const leak of ['AGE-SECRET-KEY', 'sk-', 'xoxb-']) expect(text).not.toContain(leak)
  })
})
