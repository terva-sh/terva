import { t } from '../../i18n'

// Relative "time ago" for the Stage chat lists — turns an RFC3339 timestamp
// (SessionInfo.updated/created, from the daemon's ctrlTimeString) into a compact
// label: just now / 5m / 3h / 2d, then a short calendar date past a week. Empty
// or unparseable input yields '' so a caller can omit the stamp entirely.
export function relativeTime(iso?: string): string {
  if (!iso) return ''
  const then = new Date(iso).getTime()
  if (Number.isNaN(then)) return ''
  const secs = Math.max(0, Math.round((Date.now() - then) / 1000))
  if (secs < 45) return t('just now')
  const mins = Math.round(secs / 60)
  if (mins < 60) return t('%dm ago', mins)
  const hours = Math.round(mins / 60)
  if (hours < 24) return t('%dh ago', hours)
  const days = Math.round(hours / 24)
  if (days < 7) return t('%dd ago', days)
  return new Date(iso).toLocaleDateString(undefined, { month: 'short', day: 'numeric' })
}
