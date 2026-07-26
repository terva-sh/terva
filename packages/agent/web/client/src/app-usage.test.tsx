// @vitest-environment happy-dom
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { act, cleanup, render } from '@testing-library/preact'

import { App, ContextBody, statusWindows } from './app'
import { fakeClient } from './platform/ctrlproto/testing'
import type { ContextBreakdown, UsageInfo, UsageWindow } from './platform/ctrlproto/types'

// The usage pane's subscription windows.
//
// The defect these cover: ContextBreakdown.usage_windows is a PASSIVE read of
// whatever the provider client already observed, and only the header-family
// providers (anthropic, codex) observe anything that way. A poll-family one
// (kimi, openrouter, deepseek) reports nothing until its usage endpoint is
// called, and nothing on the web ever called it — so a kimi subscription showed
// no windows at all while the TUI, which has always mirrored usage.snapshot,
// showed them. The verb was in the client's Verb union the whole time, never
// sent.

const breakdown = (over: Partial<ContextBreakdown> = {}): ContextBreakdown => ({
  window: 200000,
  system_bytes: 0,
  ext_guidance_bytes: 0,
  tool_bytes: 0,
  tool_count: 0,
  ext_bytes: 0,
  transcript_bytes: 0,
  total_bytes: 0,
  messages: [],
  cumulative: { input: 0, output: 0, cache_read: 0, cache_write: 0, cost_usd: 0 },
  ...over,
})

const win = (label: string, pct: number, kind = 'plan'): UsageWindow => ({
  label,
  used_percent: pct,
  kind,
})

describe('statusWindows', () => {
  it('prefers the breakdown, which a header-family provider keeps warm per turn', () => {
    const mirror: UsageInfo = { has_data: true, windows: [win('weekly', 99)] }
    expect(statusWindows(breakdown({ usage_windows: [win('5h', 12)] }), mirror)).toEqual([win('5h', 12)])
  })

  // The whole point: an empty breakdown is the poll-family provider's normal
  // state, and before the mirror it left the pane blank.
  it('falls back to the usage.snapshot mirror when the breakdown has none', () => {
    const mirror: UsageInfo = { has_data: true, windows: [win('5h', 40), win('weekly', 8)] }
    expect(statusWindows(breakdown(), mirror)).toEqual([win('5h', 40), win('weekly', 8)])
  })

  // usage.snapshot deliberately returns EVERY window the provider reports and
  // leaves filtering to the caller, while the breakdown arrives pre-filtered.
  // Without this the pane would show throughput limits for a poll-family
  // provider and not for a header-family one — one pane, two meanings.
  it('drops rate-limit windows from the mirror, as the breakdown already does', () => {
    const mirror: UsageInfo = { has_data: true, windows: [win('rpm', 90, 'rate_limit'), win('weekly', 8)] }
    expect(statusWindows(breakdown(), mirror)).toEqual([win('weekly', 8)])
  })

  it('is undefined when neither source has a window to show', () => {
    expect(statusWindows(breakdown(), null)).toBeUndefined()
    expect(statusWindows(breakdown(), { has_data: true, windows: [] })).toBeUndefined()
    // A provider reporting only rate-limit windows has nothing for this pane —
    // and must not render an empty meter block.
    expect(statusWindows(breakdown(), { has_data: true, windows: [win('rpm', 90, 'rate_limit')] })).toBeUndefined()
  })
})

describe('ContextBody usage meters', () => {
  afterEach(cleanup)

  const noop = async () => {
    throw new Error('not called')
  }

  it('renders a mirrored window the breakdown never carried', () => {
    const { container } = render(
      <ContextBody
        d={breakdown()}
        usage={{ has_data: true, windows: [win('weekly', 42)] }}
        onFetchNode={noop}
        onListResets={async () => ({ supported: false })}
        onConsumeReset={noop}
      />,
    )
    expect(container.querySelector('.ctx-windows')).not.toBeNull()
    expect(container.textContent).toContain('42%')
  })

  // The pre-mirror behaviour, kept: no windows anywhere means no meter block,
  // not an empty one.
  it('renders no window block when there is nothing to report', () => {
    const { container } = render(
      <ContextBody
        d={breakdown()}
        usage={null}
        onFetchNode={noop}
        onListResets={async () => ({ supported: false })}
        onConsumeReset={noop}
      />,
    )
    expect(container.querySelector('.ctx-windows')).toBeNull()
  })
})

// The wiring the component tests cannot see: that the verb is sent AT ALL. This
// is the assertion that would have caught the original defect, where every
// rendering path was correct and nothing ever asked the daemon for the data.
describe('App usage.snapshot wiring', () => {
  const SESS = 'f35bb34caca0a141'

  beforeEach(() => {
    const mem = () => {
      const store = new Map<string, string>()
      return {
        getItem: (k: string) => store.get(k) ?? null,
        setItem: (k: string, v: string) => void store.set(k, String(v)),
        removeItem: (k: string) => void store.delete(k),
        clear: () => store.clear(),
        key: (i: number) => [...store.keys()][i] ?? null,
        get length() {
          return store.size
        },
      }
    }
    vi.stubGlobal('localStorage', mem())
    const tab = mem()
    // The tab's remembered session is what makes the panel adopt one on connect
    // instead of landing on the picker (see pickBootTarget).
    tab.setItem('terva_tab_session', SESS)
    vi.stubGlobal('sessionStorage', tab)
  })

  afterEach(() => {
    cleanup()
    vi.unstubAllGlobals()
  })

  async function openUsagePane() {
    const client = fakeClient({
      respond: (method) => (method === 'sessions.list' ? { sessions: [{ id: SESS, title: 'wave 3' }] } : {}),
    })
    render(<App createClient={() => client} />)
    await act(async () => {
      client.onReady({ features: ['workspace-events'] } as never)
      for (let i = 0; i < 8; i++) await Promise.resolve()
    })
    const btn = document.querySelector('button[title="Panes (usage, settings, extensions)"]') as HTMLElement | null
    expect(btn, 'the panes button must be present once a session is adopted').not.toBeNull()
    await act(async () => {
      btn!.click()
      for (let i = 0; i < 8; i++) await Promise.resolve()
    })
    return client
  }

  it('asks the provider for a fresh snapshot when the usage pane opens', async () => {
    const client = await openUsagePane()
    const sent = client.sent('usage.snapshot')
    expect(sent.length, 'opening the usage pane sent no usage.snapshot').toBeGreaterThan(0)
    // refresh=true is the whole point: refresh=false returns the same empty
    // passive snapshot the breakdown already carries, which is the bug.
    expect(sent[0].params).toEqual({ refresh: true })
    expect(sent[0].sess, 'usage.snapshot is a session-group verb').toBe(SESS)
  })

  // A poll-family provider's numbers would otherwise freeze at whatever they
  // read when the pane opened: the daemon caches the fetch and never re-fetches
  // unless asked again.
  it('re-polls on a usage event while the pane is open', async () => {
    const client = await openUsagePane()
    const before = client.sent('usage.snapshot').length
    await act(async () => {
      client.emit(SESS, { type: 'usage', usage: { input: 10 } } as never)
      for (let i = 0; i < 4; i++) await Promise.resolve()
    })
    expect(client.sent('usage.snapshot').length).toBeGreaterThan(before)
  })
})
