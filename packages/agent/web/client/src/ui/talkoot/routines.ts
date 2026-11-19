import { t } from '../../i18n'
import type { TalkootRoutineFields } from '../../platform/ctrlproto/types'

// The six limits of a routine, in the order a card and a list show them.
export const LIMIT_FIELDS = [
  'spend_usd_per_day',
  'reviews_per_day',
  'follow_ups',
  'catch_up',
  'end_after_runs',
  'end_after_days',
] as const

// limitLabel names one limit of a routine.
export function limitLabel(field: string): string {
  switch (field) {
    case 'spend_usd_per_day':
      return t('Spend in one day')
    case 'reviews_per_day':
      return t('Review requests in one day')
    case 'follow_ups':
      return t('Follow-ups after one run')
    case 'catch_up':
      return t('After a missed time')
    case 'end_after_runs':
      return t('Ends after this many runs')
    case 'end_after_days':
      return t('Ends after this many days')
  }
  return field
}

// limitValue writes one limit as a person reads it. The daemon sends each
// value as text, so a card can show a value beside its default.
export function limitValue(field: string, value: string): string {
  switch (field) {
    case 'spend_usd_per_day':
      return `$${value}`
    case 'catch_up':
      return value === 'true' ? t('one late run') : t('no run')
    case 'end_after_runs':
    case 'end_after_days':
      return value === '0' ? t('no end') : value
  }
  return value
}

// reviewLimitNote says whether the daemon keeps the review limit of a
// routine. It counts a review request only where the user configuration
// names the commands that ask for one, and a limit that nothing counts must
// not read as a limit.
export function reviewLimitNote(counted?: boolean): string {
  return counted
    ? t('Each review request in the chain of a routine counts against its review limit, and the request past the limit is refused.')
    : t('Nothing counts a review request, so the review limit binds nothing. The talkoot_routines.review_commands key names the commands that ask for a review.')
}

// usdText writes an amount with two decimals, or with every digit it has when
// two do not hold it. A limit of 5.004 then never reads as 5.00.
export function usdText(v: number): string {
  const short = v.toFixed(2)
  return Number(short) === v ? short : String(v)
}

// routineText is the text that a routine's kind takes: a watcher's command,
// and the prompt of a brief or a task. It reads the kind and never falls
// back to the other field, so a card cannot show one text for another.
export function routineText(r: { kind: string; prompt?: string; command?: string }): string {
  return (r.kind === 'watcher' ? r.command : r.prompt) ?? ''
}

// limitRows lists every limit of a stored routine as text.
export function limitRows(f: TalkootRoutineFields): { field: string; value: string }[] {
  const text: Record<string, string> = {
    spend_usd_per_day: usdText(f.spend_usd_per_day),
    reviews_per_day: String(f.reviews_per_day),
    follow_ups: String(f.follow_ups),
    catch_up: String(f.catch_up),
    end_after_runs: String(f.end_after_runs),
    end_after_days: String(f.end_after_days),
  }
  return LIMIT_FIELDS.map((field) => ({ field, value: limitValue(field, text[field]) }))
}

// kindName names a kind of routine.
export function kindName(kind: string): string {
  switch (kind) {
    case 'watcher':
      return t('watcher')
    case 'brief':
      return t('brief')
    case 'task':
      return t('task')
  }
  return kind
}
