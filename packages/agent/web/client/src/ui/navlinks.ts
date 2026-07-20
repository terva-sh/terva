// Cross-app deep links between the panel (/) and Stage (/stage/).
//
// The two apps are separate documents in separate service-worker scopes, so
// moving between them is a full navigation: the WebSocket closes and the
// destination reconnects from scratch. Neither app has a router, and both boot
// to a default — the panel to its `current` session, Stage to the library. That
// made round-tripping to read a session's context a manual hunt through the
// other app's list.
//
// The session travels as a QUERY PARAM rather than a path segment. A path would
// drag in the service worker's navigation handling, where `navigateFallback` is
// deliberately left undefined so a cache-first NavigationRoute cannot swallow
// the daemon's 401 login form (see vite.config.ts). A query param touches none
// of that machinery.
//
// Links are bare of any `?token=`: the daemon mints a cookie when a token is
// first presented, so the destination is already authenticated, and copying
// tokens into more URLs only widens where they can leak.

// Session ids are `<date>-<time>-<hex>` (core.Session's filename stem). Anything
// else arrived from a hand-edited URL and is ignored rather than pushed into
// app state.
const SESSION_ID = /^[0-9]{8}-[0-9]{6}-[0-9a-f]{8}$/

// Panes are a small closed vocabulary on the panel side; only the ones a link is
// allowed to open are listed, so a crafted URL cannot address arbitrary surfaces.
const PANES = new Set(['context', 'tasks', 'worktree'])

function href(base: string, params: Record<string, string | undefined>): string {
  const q = new URLSearchParams()
  for (const [k, v] of Object.entries(params)) {
    if (v) q.set(k, v)
  }
  const s = q.toString()
  return s ? `${base}?${s}` : base
}

/** Link to the control panel, optionally landing on a session and a pane. */
export function panelHref(session?: string, pane?: string): string {
  return href('/', { session, pane })
}

/** Link to Stage, optionally opening straight into a session's chat. */
export function stageHref(session?: string): string {
  return href('/stage/', { session })
}

/**
 * Read the deep-link params and REMOVE them from the address bar.
 *
 * Stripping is deliberate. The params are a one-shot instruction, not state: the
 * service worker caches navigations NetworkFirst keyed by full URL, so leaving
 * them would mint a shell-cache entry per session id; and a later reload would
 * silently drag you back to a session you had since navigated away from.
 */
export function takeNavParams(): { session: string; pane: string } {
  if (typeof location === 'undefined') return { session: '', pane: '' }
  const q = new URLSearchParams(location.search)
  const rawSession = q.get('session') ?? ''
  const rawPane = q.get('pane') ?? ''
  const session = SESSION_ID.test(rawSession) ? rawSession : ''
  const pane = PANES.has(rawPane) ? rawPane : ''
  if ((rawSession || rawPane) && typeof history !== 'undefined' && history.replaceState) {
    q.delete('session')
    q.delete('pane')
    const rest = q.toString()
    history.replaceState(null, '', location.pathname + (rest ? `?${rest}` : '') + location.hash)
  }
  return { session, pane }
}
