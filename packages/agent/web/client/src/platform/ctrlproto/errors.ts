// Turning a rejected ctrlproto call into text a human reads.
//
// This was done 100 times by hand, as `String(e)`, and every one of them was
// wrong the same two ways. The client rejected with
// `new Error(`${code}: ${message}`)`, baking the machine code into the prose;
// `String(e)` then prepended JavaScript's own "Error: ". A busy session
// rendered as:
//
//	Error: busy: a turn is already running
//
// — two layers of machine prefix on a sentence the daemon marked with i18n.M
// specifically so it could be translated. And because none of the 100 sites
// used the client's own `t()`, every one of those surfaces was untranslatable
// regardless of what the translator did to the web catalogue.
//
// The code is kept as a FIELD rather than concatenated, so a caller that needs
// to branch on it can (see isWireCode) without matching prose. That coupling
// was already load-bearing and already fragile: restart.ts compared
// `msg === 'connection closed'`, so rewording that string in client.ts would
// have turned every successful restart into an error toast.

import { t } from '../../i18n'

// Codes the daemon sends. Mirrors packages/agent/ctrlproto/wire.go — the two
// are held together by a test rather than by hope (see errors.test.ts).
export const WireCodes = {
  busy: 'busy',
  noSession: 'no_session',
  notFound: 'not_found',
  badRequest: 'bad_request',
  unsupported: 'unsupported',
  unauthorized: 'unauthorized',
  conflict: 'conflict',
  internal: 'internal',
} as const

// Client-side codes for failures that never reach the daemon. Namespaced with
// a leading "client." so they can never collide with a wire code, whatever the
// daemon adds later.
export const ClientCodes = {
  connectionClosed: 'client.connection_closed',
  notConnected: 'client.not_connected',
} as const

// WireError is a rejected ctrlproto call: a stable machine code and the
// daemon's own already-translated sentence, kept apart.
//
// `message` (Error's own field) stays "<code>: <detail>" so anything that logs
// the raw error still says which code it was. Human-facing text must come from
// errText, never from `.message`.
export class WireError extends Error {
  readonly code: string
  readonly detail: string

  constructor(code: string, detail: string) {
    super(detail ? `${code}: ${detail}` : code)
    this.name = 'WireError'
    this.code = code
    this.detail = detail
  }
}

// isWireCode reports whether e is a WireError carrying code.
//
// The point is that callers branch on the CODE. Comparing prose is what made
// restart.ts depend on the exact wording of a string in another module, with
// nothing type-checking the link.
export function isWireCode(e: unknown, code: string): boolean {
  return e instanceof WireError && e.code === code
}

// Text for the codes this module synthesizes itself. Their "detail" is a
// placeholder written a few lines up in client.ts, so there is no authority to
// defer to and the friendlier line always wins.
const clientText: Record<string, () => string> = {
  [ClientCodes.connectionClosed]: () => t('the connection to terva dropped'),
  [ClientCodes.notConnected]: () => t('not connected to terva'),
}

// Text for a DAEMON code that arrives bare — no detail, or a detail that is
// just the code again. Only a fallback: see errText.
const bareCodeText: Record<string, () => string> = {
  [WireCodes.unauthorized]: () => t('you are not signed in'),
  [WireCodes.internal]: () => t('terva hit an unexpected error'),
  [WireCodes.busy]: () => t('a turn is already running'),
  [WireCodes.noSession]: () => t('that session is gone'),
  [WireCodes.notFound]: () => t('not found'),
}

// errText renders any thrown value as a sentence for a human.
//
// Never String(e): that is what produced the "Error: " prefix on 100 surfaces.
//
// 🪤 The daemon's own detail OUTRANKS anything in bareCodeText, and getting
// that backwards is worse than the bug this replaces. An earlier version keyed
// on the code first, so `internal: restart: cannot self-restart a "go run"
// build` — the one sentence that tells the operator why the restart button did
// nothing — was replaced by "terva hit an unexpected error". The daemon's
// message is specific AND already translated on its way out (serveState.fail
// translates the i18n.M-marked text), so re-deriving it here would be a second
// catalogue to keep in step with the Go one.
export function errText(e: unknown): string {
  if (e instanceof WireError) {
    const client = clientText[e.code]
    if (client) return client()
    const detail = e.detail.trim()
    if (detail && detail !== e.code) return detail
    const bare = bareCodeText[e.code]
    return bare ? bare() : e.code
  }
  if (e instanceof Error && e.message) return e.message
  if (typeof e === 'string' && e) return e
  return t('something went wrong')
}
