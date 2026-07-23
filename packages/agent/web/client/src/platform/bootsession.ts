// Which session a freshly-connected panel adopts on boot.
//
// The invariant here IS the fix for the concurrent-client bug: a tab never falls
// back to a workspace-global "current" session. The server's `current` is the
// most-recently-modified session on disk, shared by every client — so when the
// panel used to adopt it, two independently-opened clients drove ONE session and
// a model switch or login in one moved the other. Precedence is instead:
//
//   1. an explicit `?session=` deep link (cross-app navigation from Stage), then
//   2. this TAB's own remembered session (sessionStorage — per tab, never shared),
//   3. otherwise '' — the session-focused landing, adopting nothing.
//
// Both candidates must still exist in the live session set; a stale id (deleted
// session, hand-edited URL) falls through to the next rung.
export function pickBootTarget(opts: { linked: string; remembered: string; sessionIds: readonly string[] }): string {
  const present = (id: string) => !!id && opts.sessionIds.includes(id)
  if (present(opts.linked)) return opts.linked
  if (present(opts.remembered)) return opts.remembered
  return ''
}
