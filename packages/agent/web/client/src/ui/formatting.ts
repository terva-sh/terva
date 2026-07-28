import { t } from '../i18n'

// humanBytes is the ONE byte formatter. There were three — this one, the
// archive browser's (`2K`, `5.0M`) and Stage's (`4 KB`, `512 bytes`) — so the
// same file could be a different size depending on which panel you read it in,
// and the drawer showed two of the three formats a few rows apart.
//
// This shape wins because it is what the most callers already render and what
// the newest code (the attachment chips and labels) adopted. Stage's units were
// the only translated ones, so that marking is what came across rather than
// being dropped: a unit suffix is not universally English.
//
// ⚠️ t() is render-time. Every caller must be inside a render or a handler —
// hoisting this into a module-level constant would freeze the units at the
// bundle's language and stop following a live locale change.
export function humanBytes(n: number): string {
  if (n >= 1 << 20) return t('%s MB', (n / (1 << 20)).toFixed(1))
  if (n >= 1 << 10) return t('%s KB', (n / (1 << 10)).toFixed(1))
  return t('%d B', n)
}

export function humanCount(n: number): string {
  if (n >= 1_000_000) return (n / 1e6).toFixed(1) + 'M'
  if (n >= 1_000) return (n / 1e3).toFixed(0) + 'k'
  return String(n)
}

export function compact(v: unknown): string {
  try {
    return truncate(JSON.stringify(v), 200)
  } catch {
    return ''
  }
}

export function truncate(s: string, n: number): string {
  return s.length > n ? s.slice(0, n) + '…' : s
}

// localInstant renders an RFC 3339 wire instant in the VIEWER's timezone and
// locale.
//
// Every instant on the wire is UTC — the daemon formats them with
// time.RFC3339 and that is the only sane thing for it to do, since it has no
// idea where the browser is. Reading one back by slicing the string, as the
// reset-credit row did, keeps the UTC calendar date: a credit expiring at
// 01:30Z on the 2nd was shown as "expires 2026-08-02" to someone for whom it
// was still the evening of the 1st, and the deadline arrives a day earlier
// than the label reads. Slicing also drops the time of day entirely, which for
// a scarce, irreversible credit is the part that decides whether you still
// have tonight to spend it.
//
// Date and time, no seconds — a deadline, not a log line. Blank for a missing
// or unparseable value, so a caller can fall back rather than print "Invalid
// Date". `over` exists so a test can pin a timezone; production passes nothing.
export function localInstant(iso?: string, over: Intl.DateTimeFormatOptions = {}): string {
  if (!iso) return ''
  const d = new Date(iso)
  if (isNaN(d.getTime())) return ''
  return d.toLocaleString(undefined, { dateStyle: 'medium', timeStyle: 'short', ...over })
}

// clockTime renders a wire instant as a bare time of day in the viewer's
// timezone and locale — "14:32", "2:32 PM", whichever that locale uses.
//
// Deliberately absolute rather than relative ("4m ago"): a relative stamp has to
// re-render to stay true, and the transcript rows are memoized precisely so they
// do NOT re-render while a reply streams. An absolute stamp is right the moment
// it is painted and stays right. The elapsed sense comes from the gap markers
// between rows instead (see humanGap).
//
// No date and no seconds — the row is a glance, not a log line. Callers pair it
// with localInstant in a title when the full instant matters.
export function clockTime(iso?: string): string {
  if (!iso) return ''
  const d = new Date(iso)
  if (isNaN(d.getTime())) return ''
  return d.toLocaleTimeString(undefined, { hour: 'numeric', minute: '2-digit' })
}

// humanGap renders a duration as the coarsest unit that still says something:
// "12m", "3h", "2d". One unit, never "3h 12m" — the point is the order of
// magnitude ("did I step away, or did that reply take a while"), and a second
// unit costs width without changing the answer. Floors at "1m", because the
// marker only appears above a threshold well past a minute anyway.
export function humanGap(ms: number): string {
  const mins = Math.floor(ms / 60000)
  if (mins < 60) return `${Math.max(1, mins)}m`
  const hours = Math.floor(mins / 60)
  if (hours < 24) return `${hours}h`
  return `${Math.floor(hours / 24)}d`
}
