// How busy a session is, and the grouping that follows from it.
//
// The three-state vocabulary (busy / idle / cold) was born on the board as an
// expression inlined in SessionsBoard's render. The drawer now groups by the
// same states, so the rule lives here instead: one definition, unit-tested
// (vitest runs in CI), that both surfaces read. Pure — no Preact, no clock of
// its own.
//
// ⚠️ `live` does NOT mean "recent". It means the daemon holds the session
// materialized in memory (ctrlproto SessionInfo.Live), which a restart or an
// eviction clears without the transcript changing at all. Grouping on `live`
// alone would file the session you were in five minutes ago under COLD and then
// collapse it out of sight — which is exactly the moment you want it. So a
// session the daemon has forgotten still counts as idle while its transcript is
// recent, off the mtime the list already carries.

import type { SessionInfo } from './ctrlproto/types'

export type SessionStatus = 'busy' | 'idle' | 'cold'

// How long a forgotten session goes on counting as idle. A day is chosen to
// cover the shape this exists for — you closed the laptop, the daemon restarted,
// you came back to the same handful of sessions — without letting a week of
// one-off probes back into the ungrouped list.
export const IDLE_WINDOW_MS = 24 * 60 * 60 * 1000

// Sections is the list split three ways, each preserving the order it came in.
// sessions.list is sorted mtime-descending daemon-side (core.ListSessions), so
// "most recent first" inside a section costs nothing and re-sorting here would
// only risk contradicting it.
export interface Sections {
  busy: SessionInfo[]
  idle: SessionInfo[]
  cold: SessionInfo[]
}

// sessionStatus classifies one session.
//
// `streamed` is that session's busy flag from a live subscription, when one
// exists — authoritative and at turn latency, where SessionInfo.busy is only as
// fresh as the last list. Being subscribed at all also proves the session is
// live, which is why an ABSENT streamed value and a `false` one are different
// answers here and must not be collapsed to a boolean.
//
// `now` is a parameter rather than a Date.now() call so the window boundary is
// testable from both sides.
export function sessionStatus(s: SessionInfo, streamed: boolean | undefined, now: number): SessionStatus {
  if (streamed ?? !!s.busy) return 'busy'
  if (streamed !== undefined || s.live) return 'idle'
  return recentlyTouched(s, now) ? 'idle' : 'cold'
}

// recentlyTouched reads the transcript's mtime. ABSENT (an older daemon, or a
// stat that failed) is NOT read as "now" — inventing a timestamp would rescue
// every session and empty the cold group. It degrades to the live-state-only
// answer, which is what this surface did before the window existed.
function recentlyTouched(s: SessionInfo, now: number): boolean {
  if (!s.updated) return false
  const at = Date.parse(s.updated)
  return Number.isFinite(at) && now - at < IDLE_WINDOW_MS
}

// sectionSessions splits a list into the three groups. liveBusy is the caller's
// map of subscription-streamed busy flags, keyed by session id; an id absent
// from it has no subscription (see sessionStatus).
export function sectionSessions(
  sessions: SessionInfo[],
  liveBusy: Record<string, boolean> | undefined,
  now: number,
): Sections {
  const out: Sections = { busy: [], idle: [], cold: [] }
  for (const s of sessions) out[sessionStatus(s, liveBusy?.[s.id], now)].push(s)
  return out
}

// coldStartsOpen decides whether the cold group renders expanded on first paint.
//
// Collapsing it is the whole point — but a drawer whose every section is empty
// except one collapsed disclosure is a drawer that has hidden everything it
// had. When nothing is busy or idle (a fresh page against a daemon that holds
// no session yet, the common case in focus mode), cold IS the list and opens.
//
// ⚠️ Callers must seed component state with this ONCE, at mount, and let the
// user own it from there. Recomputing it every render means the group snaps
// shut under the reader the instant any session goes busy.
export function coldStartsOpen(sections: Sections): boolean {
  return sections.busy.length === 0 && sections.idle.length === 0
}
