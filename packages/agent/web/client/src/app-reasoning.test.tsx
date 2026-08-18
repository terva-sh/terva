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
//
// What a session INHERITS arrives on the current model's row, already resolved
// by the daemon, rather than being reassembled here out of a global and a raw
// per-model field. The panel used to fetch the settings surface for the global
// alone; the level it named was therefore right only when the global happened
// to be the layer that won.
function clientWith(opts: {
  reasoning?: string
  inherit?: string
  inheritFrom?: 'session' | 'model_operator' | 'global' | 'model_catalog'
}) {
  return fakeClient({
    respond: (method: string) => {
      switch (method) {
        case 'sessions.list':
          return { sessions: [{ ...SESSIONS[0], reasoning: opts.reasoning ?? '' }] }
        case 'models.list':
          return {
            models: [
              {
                id: 'a-model',
                provider: 'a-provider',
                current: true,
                inherit_reasoning: opts.inherit ?? '',
                inherit_reasoning_from: opts.inheritFrom,
              },
            ],
            reasoning_ladders: {},
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
  // adoption, then the models.list that names what the session inherits.
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

  // The other half: the button reports what the session is actually running at
  // instead of nothing.
  it('names what the session inherits when it has no override', async () => {
    await boot(clientWith({ inherit: 'high', inheritFrom: 'global' }))
    expect(document.querySelector('.reasoning-btn')?.textContent).toContain('high')
  })

  // The level an operator set for THIS MODEL in models.json outranks the global,
  // and the button used to be unable to say so: it was handed the global alone,
  // so it named a value that was not deciding anything while the turn ran at
  // the operator's.
  it("names an operator's per-model level, which outranks the global", async () => {
    await boot(clientWith({ inherit: 'minimum', inheritFrom: 'model_operator' }))
    expect(document.querySelector('.reasoning-btn')?.textContent).toContain('minimum')
  })

  // An override outranks everything below it, and is marked as deliberate.
  it('marks a session override over what it would inherit', async () => {
    await boot(clientWith({ reasoning: 'low', inherit: 'high', inheritFrom: 'global' }))
    const text = document.querySelector('.reasoning-btn')?.textContent ?? ''
    expect(text).toContain('low')
    expect(text, 'an override must be distinguishable from an inherited level').toContain('•')
  })
})

// A changed global has to move what the picker says, and the value it moves is
// no longer a cached copy of the setting — it is one input to every model row's
// resolved inherit_reasoning. So the invalidation has to hit models.list.
//
// This is the arm the mutation battery found unguarded: the panel used to
// refetch the settings surface here, and redirecting it at the model list is a
// change nothing else would notice. The picker would go on naming the level the
// user had just replaced, for the rest of the connection.
describe('a settings change refreshes what the picker reports', () => {
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

  it('refetches the model list when the settings surface changes', async () => {
    const client = clientWith({ inherit: 'high', inheritFrom: 'global' })
    await boot(client)
    const before = client.sent('models.list').length
    expect(before, 'the bootstrap never listed models — this assertion has no baseline').toBeGreaterThan(0)

    await act(async () => {
      client.emit('s1', { type: 'surface_updated', surface_id: 'settings' } as never)
    })
    for (let i = 0; i < 4; i++) {
      await act(async () => {
        await Promise.resolve()
      })
    }

    expect(
      client.sent('models.list').length,
      'the global thinking level changed and no model row was refetched — every ' +
        'row\'s inherit_reasoning is now stale, so the picker keeps naming the ' +
        'level the user just replaced',
    ).toBeGreaterThan(before)
  })

  it('does not refetch on an unrelated surface', async () => {
    const client = clientWith({ inherit: 'high', inheritFrom: 'global' })
    await boot(client)
    const before = client.sent('models.list').length

    await act(async () => {
      client.emit('s1', { type: 'surface_updated', surface_id: 'tasks' } as never)
    })
    for (let i = 0; i < 4; i++) {
      await act(async () => {
        await Promise.resolve()
      })
    }

    expect(
      client.sent('models.list').length,
      'an unrelated surface refetched the model list — the arm is not keyed on settings',
    ).toBe(before)
  })
})
