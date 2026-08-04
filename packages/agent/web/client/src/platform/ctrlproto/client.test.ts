import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { Client } from './client'
import type { Frame, Verb } from './types'

// These exercise TRANSPORT mechanics — id correlation, out-of-order responses,
// reconnect — not the verb vocabulary, so synthetic verbs are the right fixture:
// naming a real verb here would imply the test says something about that verb.
// Verb is a closed union now, so route the fakes through one cast rather than
// loosening the signature that production callers are checked against.
const verb = (v: string) => v as Verb

class FakeWebSocket {
  static readonly CONNECTING = 0
  static readonly OPEN = 1
  static readonly CLOSING = 2
  static readonly CLOSED = 3
  static instances: FakeWebSocket[] = []

  readonly url: string
  readyState = FakeWebSocket.CONNECTING
  sent: string[] = []
  onopen: (() => void) | null = null
  onmessage: ((event: MessageEvent<string>) => void) | null = null
  onclose: (() => void) | null = null

  constructor(url: string | URL) {
    this.url = String(url)
    FakeWebSocket.instances.push(this)
  }

  send(data: string) {
    this.sent.push(data)
  }

  close() {
    this.readyState = FakeWebSocket.CLOSED
  }

  open() {
    this.readyState = FakeWebSocket.OPEN
    this.onopen?.()
  }

  message(frame: Frame) {
    this.onmessage?.({ data: JSON.stringify(frame) } as MessageEvent<string>)
  }

  disconnect() {
    this.readyState = FakeWebSocket.CLOSED
    this.onclose?.()
  }
}

function socket(): FakeWebSocket {
  const ws = FakeWebSocket.instances.at(-1)
  if (!ws) throw new Error('client did not create a WebSocket')
  return ws
}

function sentFrame(ws: FakeWebSocket, index = 0): Frame {
  return JSON.parse(ws.sent[index]) as Frame
}

describe('ctrlproto Client', () => {
  beforeEach(() => {
    FakeWebSocket.instances = []
    vi.stubGlobal('WebSocket', FakeWebSocket)
    vi.stubGlobal('location', { protocol: 'http:', host: '127.0.0.1:8730', search: '' })
    vi.stubGlobal('localStorage', { getItem: vi.fn(() => null) })
  })

  afterEach(() => {
    vi.useRealTimers()
    vi.unstubAllGlobals()
  })

  it('builds the socket URL and sends the client hello on open', () => {
    vi.stubGlobal('location', { protocol: 'https:', host: 'terva.example', search: '?token=query token' })
    vi.stubGlobal('localStorage', { getItem: vi.fn(() => 'stored-token') })
    const statuses: string[] = []
    const client = new Client()
    client.onStatus = (status) => statuses.push(status)

    client.connect()
    const ws = socket()
    expect(ws.url).toBe('wss://terva.example/ws?token=query%20token')
    expect(statuses).toEqual(['connecting'])

    ws.open()
    expect(sentFrame(ws)).toEqual({
      kind: 'hello',
      hello: {
        role: 'client',
        protocol: 1,
        agent: 'terva-web',
        version: '1',
        // auth: the provider-login group. Omitting it does not merely hide the
        // feature — the daemon refuses every auth.* call with "method group not
        // negotiated", so the Providers pane renders buttons that all fail.
        // workspace-events: the #workspace address, without which every
        // workspace-scoped event (surfaces, locale, auth_state) is silently
        // dropped by the demux.
        // history-window: windowed snapshots. Without it the daemon ships the WHOLE
        // transcript — image bytes and all — on every subscribe AND at the end of
        // every turn, which is the network tax this client pays and the TUI does not.
        // secrets: the at-rest posture, on the same terms as auth — off the
        // daemon's base hello, so asking costs nothing when it is not served,
        // while not asking makes the Secrets tab's one call fail with "method
        // group not negotiated" on a daemon that WOULD have served it.
        groups: ['conversation', 'session', 'control', 'auth', 'secrets'],
        features: ['images', 'image-data', 'resolve-events', 'workspace-events', 'history-window'],
      },
    })
  })

  it('routes hello and event frames to their callbacks', () => {
    const statuses: string[] = []
    const ready = vi.fn()
    const event = vi.fn()
    const client = new Client()
    client.onStatus = (status) => statuses.push(status)
    client.onReady = ready
    client.onEvent = event

    client.connect()
    const ws = socket()
    ws.message({ kind: 'hello', hello: { role: 'server', locale: 'fi' } })
    ws.message({ kind: 'event', sess: 's1', event: { type: 'text_delta', delta: 'hei' } })

    expect(statuses).toEqual(['connecting', 'open'])
    expect(ready).toHaveBeenCalledWith({ role: 'server', locale: 'fi' })
    expect(event).toHaveBeenCalledWith('s1', { type: 'text_delta', delta: 'hei' })
  })

  it('correlates command responses and reports server errors', async () => {
    const client = new Client()
    client.connect()
    const ws = socket()
    ws.open()
    ws.sent = []

    const success = client.send<{ value: number }>(verb('example.get'), { key: 'x' }, 's1')
    expect(sentFrame(ws)).toEqual({ kind: 'cmd', id: 1, sess: 's1', method: verb('example.get'), params: { key: 'x' } })
    ws.message({ kind: 'resp', id: 1, result: { value: 7 } })
    await expect(success).resolves.toEqual({ value: 7 })

    const failure = client.send(verb('example.fail'), null)
    ws.message({ kind: 'resp', id: 2, error: { code: 'bad_request', message: 'nope' } })
    await expect(failure).rejects.toThrow('bad_request: nope')
  })

  it('rejects commands while disconnected and drops fire-and-forget calls', async () => {
    const client = new Client()
    client.connect()
    const ws = socket()

    await expect(client.send(verb('example.get'), null)).rejects.toThrow('not connected')
    client.fire(verb('example.fire'), null)
    expect(ws.sent).toEqual([])
  })

  it('rejects pending commands and reconnects after an unexpected close', async () => {
    vi.useFakeTimers()
    const statuses: string[] = []
    const client = new Client()
    client.onStatus = (status) => statuses.push(status)
    client.connect()
    const ws = socket()
    ws.open()
    const pending = client.send(verb('example.wait'), null)

    ws.disconnect()
    await expect(pending).rejects.toThrow('connection closed')
    expect(statuses).toEqual(['connecting', 'closed'])
    expect(FakeWebSocket.instances).toHaveLength(1)

    await vi.advanceTimersByTimeAsync(1500)
    expect(FakeWebSocket.instances).toHaveLength(2)
    expect(statuses).toEqual(['connecting', 'closed', 'connecting'])
    client.close()
  })

  it('waits the full 1500ms before reconnecting (not sooner)', async () => {
    vi.useFakeTimers()
    const client = new Client()
    client.connect()
    const ws = socket()
    ws.open()
    ws.disconnect()

    await vi.advanceTimersByTimeAsync(1499)
    expect(FakeWebSocket.instances).toHaveLength(1) // nothing fires early
    await vi.advanceTimersByTimeAsync(1)
    expect(FakeWebSocket.instances).toHaveLength(2) // exactly at 1500
    client.close()
  })

  it('does not reconnect after an intentional close', async () => {
    vi.useFakeTimers()
    const client = new Client()
    client.connect()
    const ws = socket()
    ws.open()
    client.close() // sets the stopped flag
    ws.disconnect() // the socket's onclose still fires; the guard must suppress reconnect

    await vi.advanceTimersByTimeAsync(5000)
    expect(FakeWebSocket.instances).toHaveLength(1)
  })

  it('routes concurrent out-of-order responses to the right caller', async () => {
    const client = new Client()
    client.connect()
    const ws = socket()
    ws.open()
    ws.sent = []

    const a = client.send<string>(verb('a'), null)
    const b = client.send<string>(verb('b'), null)
    ws.message({ kind: 'resp', id: 2, result: 'B' }) // second request answered first
    ws.message({ kind: 'resp', id: 1, result: 'A' })
    await expect(a).resolves.toBe('A')
    await expect(b).resolves.toBe('B')
  })
})
