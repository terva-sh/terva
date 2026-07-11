import type { Frame, ServerHello, Status, WireEvent } from './types'

const PROTOCOL = 1

function wsURL(): string {
  const proto = location.protocol === 'https:' ? 'wss:' : 'ws:'
  const token =
    new URLSearchParams(location.search).get('token') ||
    localStorage.getItem('terva_token') ||
    ''
  const q = token ? '?token=' + encodeURIComponent(token) : ''
  return `${proto}//${location.host}/ws${q}`
}

// Client is a reconnecting ctrlproto peer over one WebSocket. Commands return
// promises correlated by id; events are delivered to onEvent by session.
export class Client {
  private ws: WebSocket | null = null
  private nextId = 1
  private pending = new Map<number, { resolve: (v: unknown) => void; reject: (e: Error) => void }>()
  private stopped = false

  onEvent: (sess: string, ev: WireEvent) => void = () => {}
  onStatus: (s: Status) => void = () => {}
  onReady: (hello?: ServerHello) => void = () => {}

  connect() {
    this.onStatus('connecting')
    const ws = new WebSocket(wsURL())
    this.ws = ws
    ws.onopen = () =>
      ws.send(
        JSON.stringify({
          kind: 'hello',
          hello: {
            role: 'client',
            protocol: PROTOCOL,
            agent: 'terva-web',
            version: '1',
            groups: ['conversation', 'session', 'control'],
            // images = inbound attachments on prompt; image-data = outbound
            // image payloads in the transcript (agent-generated images, echoed
            // attachments, tool-result screenshots) render as real pixels.
            features: ['images', 'image-data', 'resolve-events'],
          },
        }),
      )
    ws.onmessage = (e) => this.onFrame(JSON.parse(e.data as string) as Frame)
    ws.onclose = () => {
      this.onStatus('closed')
      this.rejectAll(new Error('connection closed'))
      if (!this.stopped) setTimeout(() => this.connect(), 1500)
    }
  }

  private onFrame(f: Frame) {
    if (f.kind === 'hello') {
      this.onStatus('open')
      this.onReady(f.hello)
      return
    }
    if (f.kind === 'resp' && f.id != null) {
      const p = this.pending.get(f.id)
      if (!p) return
      this.pending.delete(f.id)
      if (f.error) p.reject(new Error(`${f.error.code}: ${f.error.message}`))
      else p.resolve(f.result)
      return
    }
    if (f.kind === 'event' && f.event) this.onEvent(f.sess ?? '', f.event)
  }

  private isOpen(): boolean {
    return this.ws != null && this.ws.readyState === WebSocket.OPEN
  }

  send<T = unknown>(method: string, params: unknown, sess = ''): Promise<T> {
    return new Promise<T>((resolve, reject) => {
      // Reject cleanly rather than letting ws.send() throw InvalidStateError on
      // a CONNECTING/closed socket (e.g. during a reconnect). Callers that care
      // catch this; the reconnect snapshot re-syncs state either way.
      if (!this.isOpen()) {
        reject(new Error('not connected'))
        return
      }
      const id = this.nextId++
      this.pending.set(id, { resolve: resolve as (v: unknown) => void, reject })
      this.raw({ kind: 'cmd', id, sess, method, params })
    })
  }

  fire(method: string, params: unknown, sess = '') {
    // Fire-and-forget: silently drop if the socket isn't open. Calling send() on
    // a CONNECTING socket throws InvalidStateError, and there's no promise here
    // to reject; a reconnect re-subscribes and re-syncs via the snapshot.
    if (!this.isOpen()) return
    this.raw({ kind: 'cmd', id: this.nextId++, sess, method, params })
  }

  private raw(f: Frame) {
    // Guarded by isOpen() at the call sites, but re-check: send() on a non-OPEN
    // socket throws a DOMException (InvalidStateError).
    if (this.ws?.readyState === WebSocket.OPEN) this.ws.send(JSON.stringify(f))
  }

  private rejectAll(e: Error) {
    for (const p of this.pending.values()) p.reject(e)
    this.pending.clear()
  }

  close() {
    this.stopped = true
    this.ws?.close()
  }
}
