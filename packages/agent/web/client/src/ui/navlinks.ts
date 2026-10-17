// Cross-app deep links between the panel (/), Stage (/stage/), and the Talkoot
// app (/talkoot/). This file is the one vocabulary every app reads and writes.
//
// The apps are separate documents in separate service-worker scopes, so moving
// between them is a full navigation: the WebSocket closes and the destination
// reconnects from scratch. No app has a router, and each boots to a default —
// the panel to its `current` session, Stage to the library, Talkoot to the last
// team's room. That made round-tripping to read a session's context a manual
// hunt through the other app's list.
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

// Session ids are `<date>-<time>-<hex>` (core.Session's filename stem). Behind a
// fleet hub every id also carries the member that owns it, as `<origin>/<id>`
// (ctrlproto.JoinFederatedID), the hub's own sessions included. The origin
// pattern mirrors ctrlproto.ValidOrigin: a lower-case host name. Anything else
// arrived from a hand-edited URL and is ignored rather than pushed into app
// state.
const SESSION_ID = /^(?:[a-z0-9](?:[a-z0-9.-]{0,61}[a-z0-9])?\/)?[0-9]{8}-[0-9]{6}-[0-9a-f]{8}$/

// Panes are a small closed vocabulary on the panel side; only the ones a link is
// allowed to open are listed, so a crafted URL cannot address arbitrary surfaces.
// Each entry is a surface id the daemon declares (workspace_surfaces.go).
const PANES = new Set(['context', 'tasks', 'worktrees'])

// Talkoot ids and member ids share one rule in Go (talkoot.ValidID, roster.go):
// a lower-case letter, then up to 31 lower-case letters, digits, or hyphens.
const TALKOOT_ID = /^[a-z][a-z0-9-]{0,31}$/

// Card ids are the store's own shape: a name slug plus a content hash, or the
// bare hash for an unnamed card (build.cardID). The store already refuses a
// separator or a "..", and this is the same refusal one step earlier — a
// hand-edited URL should not reach the wire at all.
const CARD_ID = /^[a-z0-9][a-z0-9-]{0,79}$/

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

/**
 * Link to the Talkoot app, optionally opening one team, and one member's thread
 * in it. A member without a team says nothing, so it is dropped.
 */
export function talkootHref(team?: string, member?: string): string {
  return href('/talkoot/', { team, member: team ? member : undefined })
}

/** Link to Stage, optionally opening straight into a session's chat. */
export function stageHref(session?: string): string {
  return href('/stage/', { session })
}

/** Link to Stage's character studio, opened on one character. */
export function stageCardHref(card: string): string {
  return href('/stage/', { card })
}

/**
 * Read the deep-link params and REMOVE them from the address bar.
 *
 * Stripping is deliberate. The params are a one-shot instruction, not state: the
 * service worker caches navigations NetworkFirst keyed by full URL, so leaving
 * them would mint a shell-cache entry per session id; and a later reload would
 * silently drag you back to a session you had since navigated away from.
 */
export function takeNavParams(): { session: string; pane: string; card: string } {
  const p = take({ session: (v) => SESSION_ID.test(v), pane: (v) => PANES.has(v), card: (v) => CARD_ID.test(v) })
  return { session: p.session, pane: p.pane, card: p.card }
}

/**
 * Read the Talkoot app's deep-link params, `?team=` and `?member=`, and remove
 * them from the address bar, for the reason takeNavParams gives. A member read
 * without a team is dropped: a member id names nobody until a team scopes it.
 */
export function takeTalkootParams(): { team: string; member: string } {
  const p = take({ team: (v) => TALKOOT_ID.test(v), member: (v) => TALKOOT_ID.test(v) })
  return { team: p.team, member: p.team ? p.member : '' }
}

// take reads each named param, keeps the value only when its check passes, and
// strips every named param that was present, valid or not. A rejected value
// left in the URL would be re-read on every reload.
function take<K extends string>(checks: Record<K, (v: string) => boolean>): Record<K, string> {
  const names = Object.keys(checks) as K[]
  const out = Object.fromEntries(names.map((k) => [k, ''])) as Record<K, string>
  if (typeof location === 'undefined') return out
  const q = new URLSearchParams(location.search)
  let seen = false
  for (const k of names) {
    const raw = q.get(k)
    if (raw === null) continue
    seen = true
    if (checks[k](raw)) out[k] = raw
    q.delete(k)
  }
  if (seen && typeof history !== 'undefined' && history.replaceState) {
    const rest = q.toString()
    history.replaceState(null, '', location.pathname + (rest ? `?${rest}` : '') + location.hash)
  }
  return out
}
