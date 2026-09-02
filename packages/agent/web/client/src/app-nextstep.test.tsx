// @vitest-environment happy-dom
//
// The UNBIDDEN half of suggest.next_step: the idle timer that offers a next
// step on terva's own initiative, and the suppressions that stop it.
//
// This is the half that shipped untested. It matters more than the on-demand
// path, not less: it fires unattended and spends money, so a wrong suppression
// is either silent overspending or an offer that never appears, and neither
// announces itself. Every guard below exists because firing would have been
// wrong, so every guard gets a test.
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { act, cleanup, render } from '@testing-library/preact'

import { App } from './app'
import { fakeClient, type FakeClient } from './platform/ctrlproto/testing'

const IDLE_MS = 30_000

function clientWith(opts: { nextStep?: boolean } = {}): FakeClient {
  return fakeClient({
    respond: (method, params) => {
      switch (method) {
        case 'sessions.list':
          return { sessions: [{ id: 's1', title: 'a session' }] }
        case 'surface.get':
          if ((params as { id?: string })?.id !== 'settings') return { surface: {} }
          return {
            surface: {
              settings: {
                items: [
                  { key: 'next_step', label: 'Suggest', type: 'bool', value: opts.nextStep ? 'true' : 'false' },
                ],
              },
            },
          }
        case 'suggest.next_step':
          return { line: 'run the failing test again' }
        default:
          return {}
      }
    },
  })
}

async function boot(client: FakeClient) {
  // The panel adopts NO session on a fresh tab -- it lands on the picker, where
  // there is no composer and no subscription, so every "it did not ask"
  // assertion below would pass for the wrong reason. Seeding the tab's
  // remembered session is what puts it in a session at all.
  sessionStorage.setItem('terva_tab_session', 's1')
  render(<App createClient={() => client} />)
  await act(async () => {
    client.onReady({ features: ['workspace-events'] } as never)
  })
  // Let the bootstrap's chained awaits settle: sessions.list, then adoption.
  for (let i = 0; i < 8; i++) {
    await act(async () => {
      await Promise.resolve()
    })
  }
}

// A reply landing is what arms the window.
async function replyEnds(client: FakeClient) {
  await act(async () => {
    client.emit('s1', { type: 'turn_end' } as never)
    await Promise.resolve()
  })
}

async function idle(ms: number) {
  await act(async () => {
    vi.advanceTimersByTime(ms)
    await Promise.resolve()
    await Promise.resolve()
  })
}

const asks = (client: FakeClient) => client.sent('suggest.next_step')

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
  vi.useFakeTimers()
})

afterEach(() => {
  vi.useRealTimers()
  cleanup()
  sessionStorage.clear()
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
})

// A guard on the harness itself. Every suppression test below asserts an
// ABSENCE, which is exactly the shape that passes when the fixture is broken --
// and this one WAS broken first time out, sitting on the session picker where
// nothing could ever ask. If the composer is not on screen, the negative tests
// below mean nothing, so say so once, loudly, here.
async function bootInSession(client: FakeClient) {
  await boot(client)
  expect(
    document.querySelector('.composer textarea'),
    'the panel is in a session (otherwise every absence assertion is vacuous)',
  ).toBeTruthy()
}

describe('the unbidden next-step offer', () => {
  it('asks once the user has been quiet for the idle window', async () => {
    const client = clientWith({ nextStep: true })
    await bootInSession(client)
    await replyEnds(client)

    await idle(IDLE_MS + 100)

    expect(asks(client)).toHaveLength(1)
    // Unbidden, so on_demand must be absent or false: it selects the prompt
    // that tells the model the user has not asked for anything, which is true
    // on this path and false on the other.
    expect((asks(client)[0].params as { on_demand?: boolean })?.on_demand).toBeFalsy()
  })

  it('does not ask before the window has elapsed', async () => {
    const client = clientWith({ nextStep: true })
    await bootInSession(client)
    await replyEnds(client)

    await idle(IDLE_MS - 1000)

    expect(asks(client)).toHaveLength(0)
  })

  it('asks once per reply, not on a repeating timer', async () => {
    // A user who walks away for an hour costs one completion, not a hundred
    // and twenty.
    const client = clientWith({ nextStep: true })
    await bootInSession(client)
    await replyEnds(client)

    // Four windows, not forty: the panel runs a 16ms pacer interval, and fake
    // timers abort after a bounded number of callbacks, so an enormous advance
    // stops early and proves nothing.
    await idle(IDLE_MS * 4)

    expect(asks(client)).toHaveLength(1)
  })

  it('stays silent when the setting is off', async () => {
    // The whole point of the setting: it spends money on terva's initiative.
    const client = clientWith({ nextStep: false })
    await bootInSession(client)
    await replyEnds(client)

    await idle(IDLE_MS + 100)

    expect(asks(client)).toHaveLength(0)
  })

  it('stays silent when the user has started writing', async () => {
    const client = clientWith({ nextStep: true })
    await bootInSession(client)
    await replyEnds(client)

    const box = document.querySelector('.composer textarea') as HTMLTextAreaElement
    expect(box, 'composer rendered').toBeTruthy()
    await act(async () => {
      box.value = 'half a thought'
      box.dispatchEvent(new Event('input', { bubbles: true }))
      await Promise.resolve()
    })

    await idle(IDLE_MS + 100)

    expect(asks(client)).toHaveLength(0)
  })

  it('stays silent while a question or approval is pending', async () => {
    // That gate is already asking the user to decide something. Two prompts
    // about what to do next would fight.
    const client = clientWith({ nextStep: true })
    await bootInSession(client)
    await replyEnds(client)

    await act(async () => {
      client.emit('s1', {
        type: 'ask_request',
        ask: { ask_id: 'a1', question: 'Which one?', questions: [{ question: 'Which one?', options: ['a', 'b'] }] },
      } as never)
      await Promise.resolve()
    })

    await idle(IDLE_MS + 100)

    expect(asks(client)).toHaveLength(0)
  })

  // SKIPPED DELIBERATELY, and not because it fails.
  //
  // It passes -- but it also passed with the lastTurnBad guard disabled, so
  // something other than the guard is producing the zero and the test proves
  // nothing about the behaviour it names. Every other test in this file was
  // confirmed by mutating the guard it covers and watching it go red; this one
  // stayed green, which is the only reason it is parked.
  //
  // The likely cause is the fixture rather than the product: emitting a bare
  // {type:'error'} may leave the panel in a state where askNextStep returns
  // early for an unrelated reason (no current session), which would make the
  // absence meaningless. Fixing it needs a control -- prove the same sequence
  // WITHOUT the error does ask -- and that is the work left.
  //
  // The suppression itself is real and wired (see the 'error' case in the event
  // handler, which sets lastTurnBadRef and drops the offer). It is untested,
  // and saying so is worth more than a green tick that means nothing.
  it.skip('does not arm after a turn that errored', async () => {
    // Answering "here's what to do next" to a turn that just failed is the
    // wrong read of the room.
    const client = clientWith({ nextStep: true })
    await bootInSession(client)

    await act(async () => {
      client.emit('s1', { type: 'error', error: 'boom' } as never)
      await Promise.resolve()
    })
    await replyEnds(client)

    await idle(IDLE_MS + 100)

    expect(asks(client)).toHaveLength(0)
  })
})
