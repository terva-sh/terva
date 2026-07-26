// @vitest-environment happy-dom
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { act, cleanup, render, screen } from '@testing-library/preact'

import { App, countdown, CtxRow, ResetRow, shortWindow, wtStatus } from './app'
import { fakeClient } from './platform/ctrlproto/testing'
import { localInstant } from './ui/formatting'
import { ADDR_WORKSPACE } from './platform/ctrlproto/types'

// app.tsx held 47 top-level declarations behind ONE export, so 2689 lines of
// already-pure, props-in/callbacks-out components could not be imported by a
// test at all — not for want of a seam, for want of a keyword. This file is the
// proof the sweep unlocked them; it covers a component and three helpers, and
// the pattern extends to the other 43 without further refactoring.
//
// (App itself is still untestable: it builds its Client inside a useEffect with
// no injection point. That is the next seam, and a different change.)

afterEach(cleanup)

describe('wtStatus', () => {
  // Order matters here — the checks are first-match, and an unmanaged worktree
  // that is also stale must read as unmanaged, not stale.
  it('reports unmanaged ahead of every other state', () => {
    expect(wtStatus({ unmanaged: true, stale_reason: 'gone', status: 'claimed', claimed_by: 'self' } as never))
      .toEqual({ label: 'unmanaged', cls: 's-unmanaged' })
  })

  it('distinguishes a worktree this session claimed from one another did', () => {
    expect(wtStatus({ claimed_by: 'self' } as never).label).toBe('claimed(self)')
    expect(wtStatus({ status: 'claimed', claimed_by: 'agent-7' } as never).label).toBe('claimed(agent-7)')
  })

  it('falls back to available', () => {
    expect(wtStatus({} as never)).toEqual({ label: 'available', cls: 's-available' })
  })
})

describe('countdown', () => {
  afterEach(() => vi.useRealTimers())

  function at(now: string) {
    vi.useFakeTimers()
    vi.setSystemTime(new Date(now))
  }

  it('renders days and hours, then hours and minutes, then minutes', () => {
    at('2026-07-20T00:00:00Z')
    expect(countdown('2026-07-23T17:00:00Z')).toBe('3d17h')
    expect(countdown('2026-07-20T04:33:00Z')).toBe('4h33m')
    expect(countdown('2026-07-20T00:12:00Z')).toBe('12m')
  })

  // A window that has already reset renders nothing rather than a negative
  // duration — the row is meant to disappear, not to read as "-3h".
  it('is empty for an elapsed or unparseable instant', () => {
    at('2026-07-20T00:00:00Z')
    expect(countdown('2026-07-19T00:00:00Z')).toBe('')
    expect(countdown('not a date')).toBe('')
  })

  it('floors sub-minute remainders to <1m rather than 0m', () => {
    at('2026-07-20T00:00:00Z')
    expect(countdown('2026-07-20T00:00:30Z')).toBe('<1m')
  })
})

describe('shortWindow', () => {
  it('abbreviates the two known windows, case- and space-insensitively', () => {
    expect(shortWindow('weekly')).toBe('wk')
    expect(shortWindow(' Week ')).toBe('wk')
    expect(shortWindow('MONTHLY')).toBe('mo')
  })

  // Anything else is a server-supplied label and must pass through unchanged.
  it('passes an unknown label through verbatim', () => {
    expect(shortWindow('5h')).toBe('5h')
  })
})

describe('CtxRow', () => {
  it('renders the label, a human byte size, and an estimated token count', () => {
    const { container } = render(<CtxRow label="SYSTEM.md" bytes={4096} note="" />)
    expect(screen.getByText('SYSTEM.md')).toBeTruthy()
    expect(container.textContent).toContain('4.0 KB')
    // ~bytes/4 is the estimate the panel shows beside the real size, run
    // through humanCount — so 1024 tokens reads as "1k", not "1024".
    expect(container.textContent).toContain('~1k')
  })

  it('omits the note element when there is no note', () => {
    const { container } = render(<CtxRow label="x" bytes={10} note="" />)
    expect(container.querySelector('.ctx-row-note')).toBeNull()
  })
})

// A codex reset credit is scarce and irreversible, so WHEN it lapses is the
// row's load-bearing fact. It used to render `expires_at.slice(0, 10)` — the
// wire's UTC calendar date, with the time of day thrown away and the day itself
// wrong for anyone whose local date differs from UTC at that instant.
describe('ResetRow expiry', () => {
  const row = (expires_at: string, status = 'available') =>
    render(
      <ResetRow
        r={{ id: 'r1', title: 'reset credit', status, expires_at }}
        arming={false}
        busy={false}
        onArm={() => {}}
        onCancel={() => {}}
        onRedeem={() => {}}
      />,
    )

  it('renders the expiry in the viewer locale, not as the raw wire stamp', () => {
    const iso = '2026-08-02T01:30:00Z'
    const { container } = row(iso)
    expect(container.textContent).toContain(localInstant(iso))
    // The two shapes the slice produced: the bare ISO date, and no time at all.
    expect(container.textContent).not.toContain('2026-08-02T')
    expect(container.textContent).toContain(':')
  })

  // The precise instant stays reachable for anyone cross-checking against the
  // provider's own console, which reports UTC.
  it('keeps the exact wire instant as a tooltip', () => {
    const { container } = row('2026-08-02T01:30:00Z')
    expect(container.querySelector('.reset-meta')?.getAttribute('title')).toBe('2026-08-02T01:30:00Z')
  })

  // A spent credit reports that it is spent; the expiry is no longer the point.
  it('reads as spent once redeemed', () => {
    const { container } = row('2026-08-02T01:30:00Z', 'redeemed')
    expect(container.textContent).toContain('spent')
    expect(container.querySelector('.reset-btn')).toBeNull()
  })

  // "expires 2026-07-30" on a credit that lapsed last week reads as a future
  // deadline. The TUI's dialog has always switched tense here; the web did not.
  it('switches tense once the deadline has passed', () => {
    const { container } = row(new Date(Date.now() - 86_400_000).toISOString())
    expect(container.textContent).toContain('expired')
    expect(container.textContent).not.toContain('expires')
  })
})

// The urgency ramp. A row that reads the same five days out as forty minutes
// out is how a scarce, irreversible credit lapses unnoticed.
describe('ResetRow urgency', () => {
  const inHours = (h: number) => new Date(Date.now() + h * 3_600_000).toISOString()
  const rowAt = (expires_at: string, status = 'available') =>
    render(
      <ResetRow
        r={{ id: 'r1', title: 'reset credit', status, expires_at }}
        arming={false}
        busy={false}
        onArm={() => {}}
        onCancel={() => {}}
        onRedeem={() => {}}
      />,
    ).container.querySelector('.reset-row') as HTMLElement

  it('leaves a distant credit looking like any other row', () => {
    const el = rowAt(inHours(24 * 7))
    expect(el.className).not.toContain('ui-deadline')
    expect(el.getAttribute('style')).toBeFalsy()
  })

  it('highlights within a day and rings within four hours', () => {
    expect(rowAt(inHours(12)).className).toContain('ui-deadline--soon')
    expect(rowAt(inHours(2)).className).toContain('ui-deadline--urgent')
  })

  // The ramp position rides an inline custom property; ui.css mixes the colour
  // from it, so the component names no colour at all.
  it('carries the ramp position and no colour of its own', () => {
    const style = rowAt(inHours(60)).getAttribute('style') ?? ''
    expect(style).toContain('--ui-urgency')
    expect(style).not.toMatch(/#[0-9a-f]{3,6}|rgb\(/i)
  })

  // Redeemed and lapsed credits are both done with: neither should be the
  // loudest row on screen.
  it('does not mark a credit that is spent or already gone', () => {
    expect(rowAt(inHours(2), 'redeemed').className).not.toContain('ui-deadline')
    expect(rowAt(inHours(-2)).className).not.toContain('ui-deadline')
  })
})

// App itself, driven through the transport seam.
//
// Until createClient existed these were impossible to write: `new Client()` sat
// inside the mount effect, so the 1193 lines of orchestration below it — the
// event demux, the reconnect re-subscribe, the hello feature-gating — had no
// observer. What follows are the behaviours whose regressions are silent, which
// is exactly the set that had no test.
describe('App transport lifecycle', () => {
  // App reads persisted view preferences at mount (tool view, focus-vs-board).
  // happy-dom does not supply a localStorage here, so give it a real in-memory
  // one rather than a partial stub — a half-implemented Storage would fail later
  // and further from the cause.
  beforeEach(() => {
    const store = new Map<string, string>()
    vi.stubGlobal('localStorage', {
      getItem: (k: string) => store.get(k) ?? null,
      setItem: (k: string, v: string) => void store.set(k, String(v)),
      removeItem: (k: string) => void store.delete(k),
      clear: () => store.clear(),
      key: (i: number) => [...store.keys()][i] ?? null,
      get length() {
        return store.size
      },
    })
  })

  afterEach(() => {
    cleanup()
    vi.unstubAllGlobals()
  })

  // A hello carrying the workspace-events feature must produce a subscribe to
  // the workspace ADDRESS, not to a session. Workspace-scoped events (a surface
  // changing, the locale, a notice) arrive on their own address; a panel that
  // never subscribes simply stops seeing them, with nothing on screen to say so.
  it('subscribes to the workspace address when the daemon offers the feature', async () => {
    const client = fakeClient()
    render(<App createClient={() => client} />)
    expect(client.connect).toHaveBeenCalled()

    await act(async () => {
      client.onReady({ features: ['workspace-events'] } as never)
    })

    const sub = client.sent('subscribe').find((c) => c.sess === ADDR_WORKSPACE)
    expect(sub, 'no subscribe reached the workspace address').toBeTruthy()
  })

  // ...and must NOT when the daemon is too old to offer it. That daemon relays
  // workspace events into the session subscription instead, so subscribing to an
  // address it does not know is a wasted command, not a graceful degradation.
  it('does not subscribe to the workspace address without the feature', async () => {
    const client = fakeClient()
    render(<App createClient={() => client} />)

    await act(async () => {
      client.onReady({ features: [] } as never)
    })

    expect(client.sent('subscribe').some((c) => c.sess === ADDR_WORKSPACE)).toBe(false)
  })

  // The panel closes its socket on unmount. Leaking it would keep a reconnect
  // loop and its subscriptions alive behind a torn-down tree.
  it('closes the connection when it unmounts', () => {
    const client = fakeClient()
    const { unmount } = render(<App createClient={() => client} />)
    expect(client.isConnected()).toBe(true)
    unmount()
    expect(client.close).toHaveBeenCalled()
    expect(client.isConnected()).toBe(false)
  })

  // The restart control and the Stage link are both gated on hello features. A
  // panel that offers restart to a daemon without it produces an error the user
  // cannot act on; one that hides Stage when it IS enabled loses the only
  // in-product route to the other app.
  it('gates the Stage link on the hello feature', async () => {
    const client = fakeClient()
    render(<App createClient={() => client} />)

    await act(async () => {
      client.onReady({ features: [] } as never)
    })
    expect(document.querySelector('a[href*="/stage/"]')).toBeNull()

    await act(async () => {
      client.onReady({ features: ['stage'] } as never)
    })
    expect(document.querySelector('a[href*="/stage/"]')).not.toBeNull()
  })
})

// The right rail on the landing — a session-less panel.
//
// The rail is a switcher over the surfaces a SESSION offers, and every one of
// them is served through a session handle. The empty address is not "no
// session": the daemon resolves it by MINTING one, which is the concurrent-client
// bug the landing exists to avoid. So the panel was right to refuse to fetch —
// but it still opened the rail, which then sat on "loading…" forever. A control
// that opens onto nothing.
//
// These pin the wiring, which the drawer's own component test cannot see: which
// verb is sent, and which is NOT.
describe('App workspace drawer (no session)', () => {
  beforeEach(() => {
    const store = new Map<string, string>()
    vi.stubGlobal('localStorage', {
      getItem: (k: string) => store.get(k) ?? null,
      setItem: (k: string, v: string) => void store.set(k, String(v)),
      removeItem: (k: string) => void store.delete(k),
      clear: () => store.clear(),
      key: (i: number) => [...store.keys()][i] ?? null,
      get length() {
        return store.size
      },
    })
  })
  afterEach(() => {
    cleanup()
    vi.unstubAllGlobals()
  })

  const openDrawer = async () => {
    const client = fakeClient({
      respond: (method) =>
        method === 'auth.providers' ? { providers: [{ id: 'anthropic', label: 'Anthropic' }], can_login: true } : {},
    })
    render(<App createClient={() => client} />)
    await act(async () => {
      client.onReady({ features: [] } as never)
    })
    const btn = document.querySelector('button[title="Workspace (providers, about)"]') as HTMLElement | null
    expect(btn, 'the panes button must say it opens the workspace when there is no session').not.toBeNull()
    await act(async () => {
      btn!.click()
      await Promise.resolve()
      await Promise.resolve()
    })
    return client
  }

  it('asks the session-independent verb, addressed to nothing', async () => {
    const client = await openDrawer()
    const call = client.last('auth.providers')
    expect(call, 'the drawer must fetch providers without a session').toBeTruthy()
    // Addressed to nothing ON PURPOSE — serve.go ignores Sess for this verb
    // because a credential belongs to the daemon. This is what makes it safe
    // where a surface fetch is not.
    expect(call!.sess).toBe('')
  })

  it('never asks for surfaces without a session — that is what mints one', async () => {
    const client = await openDrawer()
    expect(client.sent('surfaces.list')).toHaveLength(0)
    expect(client.sent('surface.get')).toHaveLength(0)
  })

  it('renders the providers rather than hanging on "loading…"', async () => {
    await openDrawer()
    expect(document.querySelector('.pane-rail')).not.toBeNull()
    expect(screen.getByText('Anthropic')).toBeTruthy()
    expect(screen.queryByText('loading…')).toBeNull()
  })
})
