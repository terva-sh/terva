import type { Frame, ParamsFor, ServerHello, Status, Verb, WireEvent } from './types'
import { ClientCodes, WireError } from './errors'

const PROTOCOL = 1

// The bearer token, when the page was handed one explicitly. Normally there is
// none here and the cookie does the work; ?token= is the one-click link, and the
// localStorage key is a manual escape hatch (nothing in the app writes it).
export function authToken(): string {
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

// ClientLike is the transport seam: everything a consumer of ctrlproto needs,
// with no WebSocket in it.
//
// Client is a class with private fields, so TypeScript treats it nominally — a
// plain object can never satisfy it, which is why every test that wanted a stub
// wrote `{ send: vi.fn() } as unknown as Client`. That cast is not a formality:
// it disables checking entirely, so a stub could omit a method, or keep one this
// interface had since renamed, and every test using it still passed.
//
// Components depend on this; the two composition roots (app.tsx, apps/stage) are
// the only places that name the concrete Client, because they are the only ones
// that connect a socket.
export interface ClientLike {
  send<T = unknown, M extends Verb = Verb>(method: M, params: ParamsFor<M>, sess?: string): Promise<T>
  fire<M extends Verb = Verb>(method: M, params: ParamsFor<M>, sess?: string): void
  // Mutable by design: useConversation save-restores onEvent around its own
  // handler, since one socket serves whichever screen is mounted.
  onEvent: (sess: string, ev: WireEvent) => void
  onStatus: (s: Status) => void
  onReady: (hello?: ServerHello) => void
  maxUploadBytes: number
  maxAttachmentBytes: number
  canAttachFiles: boolean
}

// ConnectableClient adds the socket lifecycle. Deliberately separate from
// ClientLike: a screen sends verbs and reads events, it does not open or close
// the connection — only a composition root does. Keeping the two apart means a
// component cannot reach for connect() and a test double for a component need
// not pretend to have one.
export interface ConnectableClient extends ClientLike {
  connect(): void
  close(): void
}

// Client is a reconnecting ctrlproto peer over one WebSocket. Commands return
// promises correlated by id; events are delivered to onEvent by session.
export class Client implements ConnectableClient {
  private ws: WebSocket | null = null
  private nextId = 1
  private pending = new Map<number, { resolve: (v: unknown) => void; reject: (e: Error) => void }>()
  private stopped = false

  // The carrier's per-file upload bound, from the server hello (0 = unbounded /
  // not yet connected). Read it before posting file bytes: going over does not
  // return an error, it drops the socket, and the request dies with a generic
  // "connection closed" that names nothing the user can act on.
  maxUploadBytes = 0

  // The attachment route's per-file bound, and whether this carrier has such a
  // route at all. Both come from the hello: a daemon reached over a unix socket
  // has nowhere to POST bytes, so the composer must not offer a drop target that
  // silently goes nowhere.
  maxAttachmentBytes = 0
  canAttachFiles = false

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
            // secrets = the at-rest posture and the store's grants. Off the
            // daemon's base hello too, served only under --web-allow-secrets,
            // and asked for on the same terms as auth: intersecting costs
            // nothing when the daemon does not offer it, while NOT asking
            // guarantees "method group not negotiated" for a pane we do show.
            // The pane itself reads the negotiated groups before rendering.
            groups: ['conversation', 'session', 'control', 'auth', 'secrets'],
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
      this.rejectAll(new WireError(ClientCodes.connectionClosed, 'connection closed'))
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
      this.maxAttachmentBytes = f.hello?.max_attachment_bytes ?? 0
      this.canAttachFiles = (f.hello?.features ?? []).includes('attachments')
      this.onStatus('open')
      this.onReady(f.hello)
      return
    }
    if (f.kind === 'resp' && f.id != null) {
      const p = this.pending.get(f.id)
      if (!p) return
      this.pending.delete(f.id)
      if (f.error) p.reject(new WireError(f.error.code, f.error.message))
      else p.resolve(f.result)
      return
    }
    if (f.kind === 'event' && f.event) this.onEvent(f.sess ?? '', f.event)
  }

  private isOpen(): boolean {
    return this.ws != null && this.ws.readyState === WebSocket.OPEN
  }

  // method is a Verb, not a string: a typo'd verb is now a compile error rather
  // than a runtime rejection nobody is listening for. params is pinned for the
  // verbs VerbParams maps and stays `unknown` for the rest — see types.ts.
  send<T = unknown, M extends Verb = Verb>(method: M, params: ParamsFor<M>, sess = ''): Promise<T> {
    return new Promise<T>((resolve, reject) => {
      // Reject cleanly rather than letting ws.send() throw InvalidStateError on
      // a CONNECTING/closed socket (e.g. during a reconnect). Callers that care
      // catch this; the reconnect snapshot re-syncs state either way.
      if (!this.isOpen()) {
        reject(new WireError(ClientCodes.notConnected, 'not connected'))
        return
      }
      const id = this.nextId++
      this.pending.set(id, { resolve: resolve as (v: unknown) => void, reject })
      this.raw({ kind: 'cmd', id, sess, method, params })
    })
  }

  fire<M extends Verb = Verb>(method: M, params: ParamsFor<M>, sess = '') {
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
