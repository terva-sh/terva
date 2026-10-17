// What the session quick switcher lists for a query, and in what order.
//
// Pure, like sessionstatus.ts beside it, so the rules are unit-tested without a
// DOM. The palette renders whatever this returns and decides nothing itself.

import type { SessionInfo } from './ctrlproto/types'

export interface PaletteRow {
  session: SessionInfo
  // The span of the label the query matched, as [start, end), or null when
  // there is no query or the span cannot be placed (see filterSessions).
  match: [number, number] | null
}

export interface PaletteList {
  // Sessions that wait on the person. They come first, because the person is
  // the only one who can move them.
  waiting: PaletteRow[]
  // Every other match, in the order sessions.list sent it: newest activity
  // first (core.ListSessions sorts by mtime daemon-side).
  recent: PaletteRow[]
  // How many sessions matched, before the cap.
  matched: number
}

// The cap on rendered rows. A workspace can hold hundreds of sessions, and a
// person who has not narrowed the list by then wants the board, not a scroll.
export const PALETTE_CAP = 50

// sessionLabel is what a row shows and what a query matches: the title, or the
// id for a session with no title yet, as the drawer shows it.
export function sessionLabel(s: SessionInfo): string {
  return s.title || s.id
}

/**
 * filterSessions keeps the sessions whose label contains the query, ignoring
 * case, and splits them into the waiting and the rest.
 *
 * The match is a plain substring and not a fuzzy score. A person who knows the
 * session types a word from its title, and a substring keeps the list in
 * recency order rather than reordering it by a score they cannot see.
 */
export function filterSessions(sessions: SessionInfo[], query: string, cap = PALETTE_CAP): PaletteList {
  const q = query.trim().toLowerCase()
  const waiting: PaletteRow[] = []
  const recent: PaletteRow[] = []
  let matched = 0
  for (const session of sessions) {
    const label = sessionLabel(session)
    let match: [number, number] | null = null
    if (q) {
      const lower = label.toLowerCase()
      const at = lower.indexOf(q)
      if (at < 0) continue
      // ⚠️ Lower-casing can change a string's length (İ becomes two code
      // units), and then an index into the lower-cased copy points at the
      // wrong place in the label. The row still matched, so it stays, unmarked.
      if (lower.length === label.length) match = [at, at + q.length]
    }
    matched++
    const part = session.waiting ? waiting : recent
    part.push({ session, match })
  }
  // The cap trims the recent rows only. A waiting session far down the list
  // is the one the person most needs to find.
  return { waiting, recent: recent.slice(0, Math.max(0, cap - waiting.length)), matched }
}
