// @vitest-environment happy-dom
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { act, cleanup, render } from '@testing-library/preact'

import { App } from './app'
import { fakeClient } from './platform/ctrlproto/testing'

// The reasoning button in the panel's topbar was gated on its own label being
// non-empty:
//
//   {curSess && reasoningLabel(curSessReasoning, '') && (<button …>)}
//
// reasoningLabel returns the session's override, else the global — and the
// panel passed '' for the global. So with no override the label was empty, the
// guard was falsy, and the button did not render. The picker behind it is the
// thing that SETS an override, which made the first one unreachable: the
// control appeared only once you no longer needed it to appear.
//
// Both halves are pinned here, because "always show the button" and "say what
// the session is on" are separate promises and the fix to one can quietly
// undo the other.

const SESSIONS = [{ id: 's1', title: 'a session' }]

// The panel bootstraps by asking for several things at once; only the ones this
// behaviour reads are answered concretely.
function clientWith(opts: { reasoning?: string; global?: string }) {
  return fakeClient({
    respond: (method: string) => {
      switch (method) {
        case 'sessions.list':
          return { sessions: [{ ...SESSIONS[0], reasoning: opts.reasoning ?? '' }] }
        case 'models.list':
          return { models: [], reasoning_ladders: {} }
        case 'surface.get':
          return {
            surface: {
              id: 'settings',
              settings: {
                items: [{ key: 'reasoning', label: 'Thinking', type: 'enum', value: opts.global ?? '' }],
              },
            },
          }
        default:
          return {}
      }
    },
  })
}

async function boot(client: ReturnType<typeof fakeClient>) {
  // The panel deliberately adopts NO session on a fresh tab — it lands on the
  // picker, where there is no topbar and so no button to assert about. Seeding
  // the tab's remembered session is what puts it in a session, which is the
  // only state this control exists in.
  sessionStorage.setItem('terva_tab_session', 's1')
  render(<App createClient={() => client} />)
  await act(async () => {
    client.onReady({ features: ['workspace-events'] } as never)
  })
  // Let the bootstrap's chained awaits settle: sessions.list, the session
  // adoption, then the settings fetch that names the global.
  for (let i = 0; i < 8; i++) {
    await act(async () => {
      await Promise.resolve()
    })
  }
}

describe('the panel reasoning control', () => {
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

  // THE BUG: no override, no global — the state a fresh workspace is in, and
  // the exact state in which the control has to be reachable.
  it('offers the button when the session has no override at all', async () => {
    await boot(clientWith({}))
    const btn = document.querySelector('.reasoning-btn')
    expect(
      btn,
      'the topbar has no .reasoning-btn — with no override its label is empty, and ' +
        'gating the button on the label means the picker that would SET an override ' +
        'can never be opened',
    ).toBeTruthy()
  })

  // ...and it still says something rather than rendering an empty box.
  it('falls back to a glyph when there is nothing to name', async () => {
    await boot(clientWith({}))
    expect(document.querySelector('.reasoning-btn')?.textContent?.trim()).toBeTruthy()
  })

  // The other half: the global is read from the settings surface, so the button
  // reports what the session is actually running at instead of nothing.
  it('names the workspace global when the session inherits', async () => {
    await boot(clientWith({ global: 'high' }))
    expect(document.querySelector('.reasoning-btn')?.textContent).toContain('high')
  })

  // An override outranks the global, and is marked as deliberate.
  it('marks a session override over the global', async () => {
    await boot(clientWith({ reasoning: 'low', global: 'high' }))
    const text = document.querySelector('.reasoning-btn')?.textContent ?? ''
    expect(text).toContain('low')
    expect(text, 'an override must be distinguishable from an inherited level').toContain('•')
  })
})
