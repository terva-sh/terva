// @vitest-environment happy-dom
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { act, cleanup, fireEvent, render } from '@testing-library/preact'

import { App } from './app'
import { PACE_INTERVAL_MS } from './platform/conversation/pacer'
import { fakeClient } from './platform/ctrlproto/testing'

// terva queues texts of its own beside the ones a person types: the report on
// a batch of sub-agents is one. The daemon names the writer of each queued
// text in `queued_sources`, a list beside `queued`. The panel holds the two
// lists as one state value, and this pins that they stay in step through each
// way the queue changes: a broadcast, a text that leaves the queue for the
// transcript, and a removal the person makes.

function client() {
  return fakeClient({
    respond: (method: string) => {
      switch (method) {
        case 'sessions.list':
          return { sessions: [{ id: 's1', title: 'a session' }] }
        default:
          return {}
      }
    },
  })
}

async function settle() {
  for (let i = 0; i < 8; i++) {
    await act(async () => {
      await Promise.resolve()
    })
  }
}

async function boot(c: ReturnType<typeof fakeClient>) {
  sessionStorage.setItem('terva_tab_session', 's1')
  render(<App createClient={() => c} />)
  await act(async () => {
    c.onReady({ features: ['workspace-events'] } as never)
  })
  await settle()
}

// chips reads each queued chip as [is it the person's, its text].
function chips(): [boolean, string][] {
  return [...document.querySelectorAll('.msg.queued')].map((el) => [
    el.classList.contains('user'),
    el.querySelector('.queued-text')?.textContent?.trim() ?? '',
  ])
}

describe('the queue names the writer of each text', () => {
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

  it('keeps the sources in step with the texts', async () => {
    const c = client()
    await boot(c)

    await act(async () => {
      c.emit('s1', {
        type: 'queue_updated',
        queued: ['typed first', 'the recap', 'typed last'],
        queued_sources: ['', 'swarm:recap', ''],
      })
    })
    expect(chips()).toEqual([
      [true, 'typed first'],
      [false, 'sub-agent report the recap'],
      [true, 'typed last'],
    ])

    // The first text leaves the queue for the transcript. The recap is now
    // first, and it is still the recap.
    // A transcript event waits for the stream pacer's next tick, so the test
    // waits for more than one.
    await act(async () => {
      c.emit('s1', { type: 'user_message', message: { role: 'user', content: [{ type: 'text', text: 'typed first' }] } })
      await new Promise((resolve) => setTimeout(resolve, 4 * PACE_INTERVAL_MS))
    })
    expect(chips()).toEqual([
      [false, 'sub-agent report the recap'],
      [true, 'typed last'],
    ])

    // The person removes the typed text. queue.set says which entry stays, by
    // its text and its source, and the recap keeps its writer on the screen
    // before the daemon answers.
    const remove = document.querySelectorAll('.msg.queued [title="Remove from queue"]')
    await act(async () => {
      fireEvent.click(remove[1])
    })
    expect(chips()).toEqual([[false, 'sub-agent report the recap']])
    expect(c.last('queue.set')?.params).toEqual({ texts: ['the recap'], sources: ['swarm:recap'] })

    // A broadcast with no list of sources is a queue of typed texts.
    await act(async () => {
      c.emit('s1', { type: 'queue_updated', queued: ['only typed'] })
    })
    expect(chips()).toEqual([[true, 'only typed']])
  })

  // A person can type a text that equals a queued report. The texts cannot
  // say which of the two was removed, and the sources can.
  it('names the entry that stays when two queued texts are equal', async () => {
    const c = client()
    await boot(c)
    await act(async () => {
      c.emit('s1', { type: 'queue_updated', queued: ['X', 'X'], queued_sources: ['', 'swarm:recap'] })
    })

    const remove = document.querySelectorAll('.msg.queued [title="Remove from queue"]')
    await act(async () => {
      fireEvent.click(remove[0])
    })
    expect(chips()).toEqual([[false, 'sub-agent report X']])
    expect(c.last('queue.set')?.params).toEqual({ texts: ['X'], sources: ['swarm:recap'] })
  })

  // The message that a queued text becomes says who wrote it. With two equal
  // texts in the queue, that is what tells the panel which entry left.
  it('drops the entry with the same source when a queued text reaches the transcript', async () => {
    const c = client()
    await boot(c)
    const twoEqual = { type: 'queue_updated', queued: ['X', 'X'], queued_sources: ['', 'swarm:recap'] } as const
    const becomes = async (source?: string) => {
      await act(async () => {
        c.emit('s1', { type: 'queue_updated', queued: [...twoEqual.queued], queued_sources: [...twoEqual.queued_sources] })
      })
      await act(async () => {
        c.emit('s1', { type: 'user_message', message: { role: 'user', source, content: [{ type: 'text', text: 'X' }] } })
        await new Promise((resolve) => setTimeout(resolve, 4 * PACE_INTERVAL_MS))
      })
    }

    await becomes('swarm:recap')
    expect(chips()).toEqual([[true, 'X']])
    await becomes(undefined)
    expect(chips()).toEqual([[false, 'sub-agent report X']])
  })

  it('clears the queue with no list of sources', async () => {
    const c = client()
    await boot(c)
    await act(async () => {
      c.emit('s1', { type: 'queue_updated', queued: ['typed'] })
    })
    await act(async () => {
      fireEvent.click(document.querySelector('[title="Remove from queue"]') as Element)
    })
    expect(chips()).toEqual([])
    expect(c.last('queue.set')?.params).toEqual({ texts: [] })
  })
})
