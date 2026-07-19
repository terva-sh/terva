import type { Page, WebSocketRoute } from '@playwright/test'

// The smoke suite runs against the real built client (vite preview) with NO
// terva backend. installMockBackend intercepts the control-plane WebSocket
// (/ws) with page.routeWebSocket and plays just enough of the ctrlproto
// handshake for the app to reach its "open" state and select a session, then
// lets a test push transcript events. See ../../src/platform/ctrlproto/client.ts
// for the wire shapes this mirrors.

export const SMOKE_SESSION = 'smoke'

export type MockBackend = {
  // Resolves once the client has subscribed to the smoke session, i.e. its
  // curRef is set and pushed events will be applied (not dropped as off-session).
  subscribed: Promise<void>
  // Push one conversation event frame to the client (default: the smoke session).
  pushEvent: (event: Record<string, unknown>, sess?: string) => void
}

export type MockBackendOptions = {
  // Answer a send()-style cmd ahead of the built-in defaults; return undefined
  // to fall through. Lets a test serve extra methods (surfaces.list,
  // surface.get) without re-implementing the handshake. sess is the frame's
  // session address — some verbs (sessions.delete, note.set) carry their target
  // there rather than in params.
  respond?: (method: string, params: unknown, sess?: string) => unknown
}

export async function installMockBackend(page: Page, opts: MockBackendOptions = {}): Promise<MockBackend> {
  let current: WebSocketRoute | null = null
  let markSubscribed: () => void = () => {}
  const subscribed = new Promise<void>((r) => (markSubscribed = r))

  await page.routeWebSocket(/\/ws(\?|$)/, (ws) => {
    current = ws
    ws.onMessage((raw) => {
      let f: { kind?: string; id?: number; method?: string; params?: unknown; sess?: string }
      try {
        f = JSON.parse(String(raw))
      } catch {
        return
      }
      if (f.kind === 'hello') {
        // Minimal server hello → the client flips status to "open" and boots.
        ws.send(JSON.stringify({ kind: 'hello', hello: { protocol: 1, features: [] } }))
        return
      }
      if (f.kind === 'cmd') {
        // selectSession fires `subscribe` (a fire-and-forget cmd) once curRef is
        // set — that is our deterministic "ready for events" signal.
        if (f.method === 'subscribe') markSubscribed()
        // Only send()-style calls await a resp; fire()-style verbs (subscribe,
        // prompt, queue, …) ignore one, so a reply is harmless either way.
        if (f.id != null) {
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
  }
}

// A 1×1 transparent PNG, for exercising the image attachment path.
export const PNG_1x1_BASE64 =
  'iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAAC0lEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg=='
