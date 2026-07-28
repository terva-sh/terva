import type { Page, WebSocketRoute } from '@playwright/test'

// The smoke suite runs against the real built client (vite preview) with NO
// terva backend. installMockBackend intercepts the control-plane WebSocket
// (/ws) with page.routeWebSocket and plays just enough of the ctrlproto
// handshake for the app to reach its "open" state and select a session, then
// lets a test push transcript events. See ../../src/platform/ctrlproto/client.ts
// for the wire shapes this mirrors.

// A session id in the daemon's own format (YYYYMMDD-HHMMSS-<8 hex>).
//
// ⚠️ The shape is load-bearing, not decoration. takeNavParams validates
// `?session=` against exactly this pattern and silently drops anything else, so
// while this was the plain string 'smoke' the deep link could not be exercised
// at all — every test that tried one landed on the session picker instead.
export const SMOKE_SESSION = '20260101-120000-c0ffee01'

// The panel's boot precedence is: `?session=` deep link → this tab's remembered
// session → the landing (see platform/bootsession). A fresh tab deliberately
// adopts NOTHING — the server's global `current` is shared between clients, and
// following it made two panels drive one session. So a test that wants the
// session shell has to ask for it by name; `goto('/')` lands on the picker.
export const panelSessionURL = `/?session=${SMOKE_SESSION}`

// The ✎ on the row carrying `hasText`. Editing moved off the bubble — a click
// there is for selecting and copying — so a test that means "edit this message"
// has to press the button, the same as a reader does.
export function editButtonFor(page: Page, hasText: string) {
  return page.locator('.stage-row', { has: page.locator('.stage-bubble', { hasText }) }).locator('.stage-msgedit')
}

export type MockBackend = {
  // Resolves once the client has subscribed to the smoke session, i.e. its
  // curRef is set and pushed events will be applied (not dropped as off-session).
  subscribed: Promise<void>
  // Push one conversation event frame to the client (default: the smoke session).
  pushEvent: (event: Record<string, unknown>, sess?: string) => void
  // How many `subscribe` frames have arrived, across ALL connections. A client
  // must re-subscribe after a reconnect — server-side subscriptions are
  // per-connection and die with the socket — so this counter is what proves it.
  subscribeCount: () => number
  // Drop the socket from the server side, the way a daemon restart, a laptop
  // sleep, or a radio handoff does. The client reconnects on its own; the test
  // then asserts what it does (or fails to do) on the new connection.
  drop: () => void
  // Answer a withheld handshake (see deferHello). A no-op unless deferHello was
  // set, and safe to call before the client has connected — the reply goes out
  // as soon as its hello arrives.
  sendHello: () => void
  // Answer every parked call to a held verb (see holdMethods) with `result`, and
  // stop holding it. The result is passed in rather than recomputed, so a test
  // says plainly what the delayed answer IS.
  release: (method: string, result?: unknown) => void
}

export type MockBackendOptions = {
  // Answer a send()-style cmd ahead of the built-in defaults; return undefined
  // to fall through. Lets a test serve extra methods (surfaces.list,
  // surface.get) without re-implementing the handshake. sess is the frame's
  // session address — some verbs (sessions.delete, note.set) carry their target
  // there rather than in params.
  respond?: (method: string, params: unknown, sess?: string) => unknown
  // Hold the server hello back until sendHello() is called, so a test can watch
  // the client while it is still CONNECTING.
  //
  // Every other smoke wants the handshake to land instantly, and it does — which
  // is why the boot state went untested for so long: the mock made the window
  // where the panel has painted but knows nothing about the workspace
  // vanishingly small, exactly as a loopback daemon does. That window is where
  // the panel used to claim an empty workspace, so testing it means being able
  // to hold it open.
  deferHello?: boolean
  // Features the server hello advertises, and the attachment ceiling it names.
  // Default [] / 0 — the panel gates surfaces on these, so a test that wants one
  // (the composer's file drop, behind "attachments") has to ask for it.
  features?: string[]
  maxAttachmentBytes?: number
  // Withhold the response to these verbs until release() answers them.
  //
  // deferHello holds the WHOLE boot, which is the right tool when the surface
  // under test is the landing. It is the wrong one for anything that only
  // renders once a session is picked (the board's lanes), because picking a
  // session needs the hello. Those lanes' real window is "connected, session
  // chosen, this one verb still in flight" — and that is what this holds open.
  holdMethods?: string[]
  // Close the FIRST connection immediately; serve the second normally. The
  // client reconnects on its own after ~1.5s.
  //
  // This exists to make a race deterministic. An effect that fires once at mount
  // and sends on a still-connecting socket wins or loses purely on how fast the
  // handshake completes — on loopback (and against this mock) it often wins, so
  // a test written against the plain boot passes with the bug present. Failing
  // the first connection removes the coin flip: whatever the mount flush did, it
  // died with that socket, so only an effect that re-runs when the connection
  // comes back can produce any data at all. It is also a real scenario — a
  // daemon restarting as the panel loads.
  dropFirstConnection?: boolean
}

export async function installMockBackend(page: Page, opts: MockBackendOptions = {}): Promise<MockBackend> {
  let current: WebSocketRoute | null = null
  let markSubscribed: () => void = () => {}
  let subscribes = 0
  const subscribed = new Promise<void>((r) => (markSubscribed = r))
  // The handshake hold. `releaseHello` starts true unless the test defers it, so
  // every existing smoke keeps the instant handshake it was written against.
  // `helloPending` remembers a client hello that arrived while held, so release
  // order does not matter.
  let releaseHello = !opts.deferHello
  let helloPending = false
  const flushHello = () => {
    if (!helloPending || !current) return
    helloPending = false
    current.send(
      JSON.stringify({
        kind: 'hello',
        hello: {
          protocol: 1,
          features: opts.features ?? [],
          max_attachment_bytes: opts.maxAttachmentBytes ?? 0,
        },
      }),
    )
  }
  // Per-verb holds. `held` is what is still being withheld; `parked` is the
  // request ids waiting on each, so a verb the client retries (a poll) parks
  // every attempt and release() answers them all.
  const held = new Set(opts.holdMethods ?? [])
  const parked = new Map<string, number[]>()

  let connections = 0

  await page.routeWebSocket(/\/ws(\?|$)/, (ws) => {
    current = ws
    connections++
    if (opts.dropFirstConnection && connections === 1) {
      ws.close()
      return
    }
    ws.onMessage((raw) => {
      let f: { kind?: string; id?: number; method?: string; params?: unknown; sess?: string }
      try {
        f = JSON.parse(String(raw))
      } catch {
        return
      }
      if (f.kind === 'hello') {
        // Minimal server hello → the client flips status to "open" and boots.
        // Under deferHello it is parked until the test releases it; a reconnect
        // re-arms the same hold, so a dropped socket also stays down until asked.
        helloPending = true
        if (releaseHello) flushHello()
        return
      }
      if (f.kind === 'cmd') {
        // selectSession fires `subscribe` (a fire-and-forget cmd) once curRef is
        // set — that is our deterministic "ready for events" signal.
        if (f.method === 'subscribe') {
          subscribes++
          markSubscribed()
        }
        // Only send()-style calls await a resp; fire()-style verbs (subscribe,
        // prompt, queue, …) ignore one, so a reply is harmless either way.
        if (f.id != null) {
          const method = f.method ?? ''
          if (held.has(method)) {
            parked.set(method, [...(parked.get(method) ?? []), f.id])
            return
          }
          const result =
            opts.respond?.(f.method ?? '', f.params, f.sess) ??
            (f.method === 'models.list'
              ? { models: [] }
              : f.method === 'sessions.list'
                ? { sessions: [{ id: SMOKE_SESSION, current: true, title: 'smoke' }] }
                : {})
          ws.send(JSON.stringify({ kind: 'resp', id: f.id, result }))
        }
      }
    })
  })

  return {
    subscribed,
    pushEvent(event, sess = SMOKE_SESSION) {
      if (!current) throw new Error('mock backend: no active socket yet')
      current.send(JSON.stringify({ kind: 'event', sess, event }))
    },
    subscribeCount: () => subscribes,
    drop() {
      if (!current) throw new Error('mock backend: no active socket yet')
      current.close()
    },
    sendHello() {
      releaseHello = true
      flushHello()
    },
    release(method, result = {}) {
      held.delete(method)
      const ids = parked.get(method) ?? []
      parked.delete(method)
      if (!current) return
      for (const id of ids) current.send(JSON.stringify({ kind: 'resp', id, result }))
    },
  }
}

// A 1×1 transparent PNG, for exercising the image attachment path.
export const PNG_1x1_BASE64 =
  'iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAAC0lEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg=='
