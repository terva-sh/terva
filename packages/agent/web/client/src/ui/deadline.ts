// Deadline urgency — the shared vocabulary behind the .ui-deadline rules in
// ui.css.
//
// Anything with an expiry (a usage-reset credit, an OAuth credential) has the
// same problem: a date on its own says nothing about whether you need to act.
// The row reads identically five days out and forty minutes out, so a scarce,
// irreversible credit lapses unnoticed. This turns "when" into "how close",
// once, so every surface says it the same way instead of each inventing a
// colour.
//
// The levels are deliberately few and the thresholds deliberately round: a
// meter people glance at needs states they can name, not a continuum they must
// interpret. `ratio` carries the continuum for the colour ramp underneath —
// ui.css mixes --ui-warn toward --ui-danger by it — so a deadline warms
// steadily while still crossing two legible lines.
//
// Times come off the wire as RFC 3339 UTC and are compared as instants, so the
// viewer's timezone changes what is DISPLAYED (see localInstant) but never
// which level a deadline is in.

/** The ramp's far end: beyond this a deadline is not worth colouring. */
export const DEADLINE_RAMP_MS = 5 * 24 * 60 * 60 * 1000
/** Highlight from here in — "this is happening within the day". */
export const DEADLINE_SOON_MS = 24 * 60 * 60 * 1000
/** Ring it from here in — "act now or lose it". */
export const DEADLINE_URGENT_MS = 4 * 60 * 60 * 1000

export type DeadlineLevel =
  | 'none' // no expiry, or an unparseable one — render nothing
  | 'calm' // beyond the ramp; an ordinary row
  | 'near' // inside the ramp, warming
  | 'soon' // within a day
  | 'urgent' // within four hours
  | 'expired' // already lapsed: not urgent, just over

export interface Deadline {
  level: DeadlineLevel
  /** 0 at the far end of the ramp, 1 at the moment it lapses. Drives the tint. */
  ratio: number
  /** Milliseconds remaining; negative once lapsed, 0 when there is no deadline. */
  msLeft: number
}

const NONE: Deadline = { level: 'none', ratio: 0, msLeft: 0 }

// deadlineOf classifies an RFC 3339 instant against now. `now` is a parameter
// rather than a call to Date.now() so the thresholds are testable without
// faking the clock.
export function deadlineOf(iso?: string, now: number = Date.now()): Deadline {
  if (!iso) return NONE
  const at = new Date(iso).getTime()
  if (isNaN(at)) return NONE
  const msLeft = at - now
  // A lapsed deadline gets no urgency colour. It is not "extremely urgent", it
  // is over — painting it the loudest of all would put the most alarming row in
  // the list on the one credit nothing can be done about.
  if (msLeft <= 0) return { level: 'expired', ratio: 1, msLeft }
  const ratio = Math.min(1, Math.max(0, 1 - msLeft / DEADLINE_RAMP_MS))
  const level: DeadlineLevel =
    msLeft <= DEADLINE_URGENT_MS
      ? 'urgent'
      : msLeft <= DEADLINE_SOON_MS
        ? 'soon'
        : msLeft <= DEADLINE_RAMP_MS
          ? 'near'
          : 'calm'
  return { level, ratio, msLeft }
}

// deadlineClass is the class list a row wears for its level: the marker class
// plus a modifier, or '' for the levels that must look like every other row
// (no deadline, far off, or already gone). Callers append it to their own
// classes rather than replacing them.
export function deadlineClass(d: Deadline): string {
  if (d.level === 'none' || d.level === 'calm' || d.level === 'expired') return ''
  return `ui-deadline ui-deadline--${d.level}`
}

// deadlineStyle hands the ramp position to CSS, which mixes --ui-warn toward
// --ui-danger by it. Undefined when nothing is coloured, so a calm row carries
// no inline style at all.
export function deadlineStyle(d: Deadline): Record<string, string> | undefined {
  if (!deadlineClass(d)) return undefined
  // Two decimals: enough for a smooth ramp, short enough to read in devtools.
  return { '--ui-urgency': d.ratio.toFixed(2) }
}
