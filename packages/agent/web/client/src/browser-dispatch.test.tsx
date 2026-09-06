// @vitest-environment happy-dom
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { act, cleanup, fireEvent, render } from '@testing-library/preact'
import { App } from './app'
import { Chat } from './apps/stage/Chat'
import { fakeClient, type FakeClient } from './platform/ctrlproto/testing'
import { ClientCodes, WireError } from './platform/ctrlproto/errors'
import type { WireEvent } from './platform/ctrlproto/types'

beforeEach(() => { vi.useFakeTimers() })
afterEach(() => { cleanup(); vi.useRealTimers(); sessionStorage.clear(); localStorage.clear() })
const settle = async () => { await act(async () => { await vi.advanceTimersByTimeAsync(64) }) }

async function mount(kind: string, response?: () => unknown) {
  const client = fakeClient({ respond: (method) => {
    if (method === 'sessions.list') return { sessions: [{ id: 's1', title: 'session' }] }
    if (method === 'prompt' || method === 'queue') return response?.() ?? {}
    return {}
  } })
  sessionStorage.setItem('terva_tab_session', 's1')
  const view = render(kind === 'panel'
    ? <App createClient={() => client} />
    : <Chat client={client} sessionId="s1" onBack={() => {}} onOpenSession={() => {}} onOpenStudio={() => {}} />)
  await act(async () => { client.onReady({ role: 'server', features: ['workspace-events'] }) })
  await settle()
  await emit(client, { type: 'snapshot', snapshot: { busy: false, messages: [], session: { id: 's1', messages: 0, usage: { input: 0, output: 0, cache_read: 0, cache_write: 0, cost_usd: 0 } }, epoch: 0, base: 0, total: 0 } } as WireEvent)
  const input = view.container.querySelector('footer textarea') as HTMLTextAreaElement
  expect(input).toBeTruthy()
  return { client, input, ...view }
}
async function emit(client: FakeClient, ev: WireEvent) {
  await act(async () => { client.emit('s1', ev) })
  await settle()
}
const enter = (input: HTMLTextAreaElement, text: string) => {
  fireEvent.input(input, { target: { value: text } })
  fireEvent.keyDown(input, { key: 'Enter' })
}

for (const kind of ['panel', 'stage']) describe(`${kind} dispatch and run state`, () => {
  for (const wait of ['tool_start', 'permission_request', 'retry']) {
    it(`keeps Stop and queues a follow-up during ${wait}`, async () => {
      const { client, input, container } = await mount(kind)
      await emit(client, { type: 'turn_start' })
      await emit(client, { type: 'turn_end' })
      await emit(client, { type: wait } as WireEvent)
      expect(container.querySelector('footer')?.textContent).toContain('Stop')
      enter(input, 'follow-up')
      await settle()
      expect(client.send).toHaveBeenCalledWith('queue', { text: 'follow-up' }, 's1')
      expect(client.sent('prompt')).toHaveLength(0)
      expect(input.value).toBe('')
      const stop = [...container.querySelectorAll('footer button')].find((b) => b.textContent?.includes('Stop'))!
      fireEvent.click(stop)
      expect(client.last('cancel')?.sess).toBe('s1')
      await emit(client, { type: 'done' })
      expect(container.querySelector('footer')?.textContent).not.toContain('Stop')
    })
  }

  for (const code of [ClientCodes.notConnected, ClientCodes.connectionClosed, 'busy']) {
    it(`keeps input and reports ${code} without replay`, async () => {
      const { client, input, container } = await mount(kind, () => { throw new WireError(code, 'dispatch rejected') })
      enter(input, 'keep this')
      await settle()
      expect(input.value).toBe('keep this')
      expect(client.send).toHaveBeenCalledWith('prompt', { text: 'keep this' }, 's1')
      expect(container.textContent).toMatch(/not connected|Check the transcript|dispatch rejected/)
      await emit(client, { type: 'snapshot', snapshot: { busy: false, messages: [], session: { id: 's1', messages: 0, usage: { input: 0, output: 0, cache_read: 0, cache_write: 0, cost_usd: 0 } }, epoch: 0, base: 0, total: 0 } } as WireEvent)
      expect(client.sent('prompt')).toHaveLength(1)
      expect(input.value).toBe('keep this')
    })
  }

  it('waits for acknowledgment and preserves later writing', async () => {
    let accept!: () => void
    const { client, input } = await mount(kind, () => new Promise<void>((r) => { accept = r }))
    enter(input, 'first')
    expect(input.value).toBe('first')
    fireEvent.keyDown(input, { key: 'Enter' })
    expect(client.sent('prompt')).toHaveLength(1)
    fireEvent.input(input, { target: { value: 'later writing' } })
    await act(async () => accept())
    expect(input.value).toBe('later writing')
  })

  it('does not resurrect busy if completion precedes acknowledgment', async () => {
    let accept!: () => void
    const { client, input, container } = await mount(kind, () => new Promise<void>((r) => { accept = r }))
    enter(input, 'fast turn')
    await emit(client, { type: 'turn_start' })
    await emit(client, { type: 'done' })
    await act(async () => accept())
    expect(container.querySelector('footer')?.textContent).not.toContain('Stop')
  })

  it('does not let a paced old snapshot overwrite a newer run', async () => {
    const { client, container } = await mount(kind)
    await act(async () => {
      client.emit('s1', { type: 'text_delta', delta: 'long output '.repeat(100) })
      client.emit('s1', { type: 'snapshot', snapshot: { busy: false, messages: [], session: { id: 's1', messages: 0, usage: { input: 0, output: 0, cache_read: 0, cache_write: 0, cost_usd: 0 } }, epoch: 0, base: 0, total: 0 } } as WireEvent)
      client.emit('s1', { type: 'turn_start' })
      vi.advanceTimersByTime(10_000)
    })
    expect(container.querySelector('footer')?.textContent).toContain('Stop')
  })

  it('leaves IME confirmation to the input method', async () => {
    const { client, input } = await mount(kind)
    fireEvent.input(input, { target: { value: '未確定' } })
    fireEvent.compositionStart(input)
    fireEvent.keyDown(input, { key: 'Enter' })
    fireEvent.compositionEnd(input)
    fireEvent.keyDown(input, { key: 'Enter', keyCode: 229 })
    expect(client.sent('prompt')).toHaveLength(0)
    expect(input.value).toBe('未確定')
    fireEvent.keyDown(input, { key: 'Enter' })
    await settle()
    expect(client.sent('prompt')).toHaveLength(1)
    expect(input.value).toBe('')
  })
})

it('keeps a Stage draft across a keyed remount and a rejected acknowledgment', async () => {
  let reject!: (e: Error) => void
  const { client, input, rerender } = await mount('stage', () => new Promise<void>((_, r) => { reject = r }))
  enter(input, 'first scene')
  const chat = (sessionId: string) => <Chat key={sessionId} client={client} sessionId={sessionId} onBack={() => {}} onOpenSession={() => {}} onOpenStudio={() => {}} />
  rerender(chat('s2'))
  let active = document.querySelector('footer textarea') as HTMLTextAreaElement
  fireEvent.input(active, { target: { value: 'second scene' } })
  await act(async () => reject(new WireError(ClientCodes.connectionClosed, 'closed')))
  expect(active.value).toBe('second scene')
  rerender(chat('s1'))
  active = document.querySelector('footer textarea') as HTMLTextAreaElement
  expect(active.value).toBe('first scene')
  expect(document.body.textContent).toContain('Check the transcript')
  expect(client.sent('prompt')).toHaveLength(1)
})
