import type { Frame, ServerHello, Status, WireEvent } from './types'

const PROTOCOL = 1

// The bearer token, when the page was handed one explicitly. Normally there is
// none here and the cookie does the work; ?token= is the one-click link, and the
// localStorage key is a manual escape hatch (nothing in the app writes it).
function authToken(): string {
  return (
    new URLSearchParams(location.search).get('token') ||
    localStorage.getItem('terva_token') ||
    ''
  )
}

function tokenQuery(): string {
  const t = authToken()
  return t ? '?token=' + encodeURIComponent(t) : ''
}

function wsURL(): string {
  const proto = location.protocol === 'https:' ? 'wss:' : 'ws:'
  return `${proto}//${location.host}/ws${tokenQuery()}`
}

// credentialRejected asks, over plain HTTP, whether the daemon is turning us away
// for lack of a credential.
//
// It has to be asked separately because the WebSocket API never exposes the
// handshake's HTTP status: a 401 and an unreachable machine both arrive at the
// page as the same bare close event. Unable to tell the two apart, the client
// used to assume the recoverable one and reconnect every 1.5s forever — silently,
// with the panel showing a stale UI it could never refresh, and no way to type the
// token that would have fixed it. So ask somewhere the status code survives.
//
// It presents the SAME credential the socket does, or the two could disagree — a
// live ?token= with a dead cookie would otherwise read as "rejected" while the
// socket was connecting happily. Presenting it also re-mints the cookie, since
// the daemon sets one whenever the token itself is shown.
async function credentialRejected(): Promise<boolean> {
  try {
    const res = await fetch('/auth/status' + tokenQuery(), {
      credentials: 'same-origin',
      cache: 'no-store',
      headers: { Accept: 'application/json' },
    })
    return res.status === 401
  } catch {
    // Unreachable, not unauthorized. Keep retrying — bouncing to a login page we
    // cannot even load would replace a recoverable outage with a broken browser.
    return false
  }
}

// bounceToLogin hands the browser to the form, once. A second bounce could only
// happen if the form sent us back still unauthenticated, and looping between the
// two pages would show the user nothing at all.
let bouncing = false
function bounceToLogin() {
  if (bouncing) return
  bouncing = true
  const next = location.pathname + location.search
  location.assign('/auth?next=' + encodeURIComponent(next))
}

// Client is a reconnecting ctrlproto peer over one WebSocket. Commands return
// promises correlated by id; events are delivered to onEvent by session.
export class Client {
  private ws: WebSocket | null = null
  private nextId = 1
  private pending = new Map<number, { resolve: (v: unknown) => void; reject: (e: Error) => void }>()
  private stopped = false

  // The carrier's per-file upload bound, from the server hello (0 = unbounded /
  // not yet connected). Read it before posting file bytes: going over does not
  // return an error, it drops the socket, and the request dies with a generic
  // "connection closed" that names nothing the user can act on.
  maxUploadBytes = 0

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
            // auth = the provider-login verbs. It is OFF the daemon's base hello
            // and served only under --web-allow-login, so asking for it costs
            // nothing when the daemon does not offer it (Negotiate intersects, and
            // the pane reads can_login before showing a single control) — but NOT
            // asking for it means every login call is refused with "method group
            // not negotiated", which is a Providers pane whose buttons all fail.
            groups: ['conversation', 'session', 'control', 'auth'],
            // images = inbound attachments on prompt; image-data = outbound
            // image payloads in the transcript (agent-generated images, echoed
            // attachments, tool-result screenshots) render as real pixels.
            // workspace-events = we understand the #workspace address, so the
            // daemon sends workspace-scoped events there once instead of
            // stamping a copy onto every session we happen to be subscribed to.
            features: ['images', 'image-data', 'resolve-events', 'workspace-events', 'history-window'],
          },
        }),
      )
    ws.onmessage = (e) => this.onFrame(JSON.parse(e.data as string) as Frame)
    ws.onclose = () => {
      this.onStatus('closed')
      this.rejectAll(new Error('connection closed'))
      if (!this.stopped) void this.reconnect()
    }
  }

  // A drop is either "we lost our credential" or "we lost the daemon", and the
  // close event does not say which. Find out before deciding what to do about it.
  private async reconnect() {
    if (await credentialRejected()) {
      bounceToLogin()
      return
    }
    if (!this.stopped) setTimeout(() => this.connect(), 1500)
  }

  private onFrame(f: Frame) {
    if (f.kind === 'hello') {
      this.maxUploadBytes = f.hello?.max_upload_bytes ?? 0
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
