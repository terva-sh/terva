import type { WireEvent } from '../ctrlproto/types'

// A model segment can end before tools, approvals, or retry waits finish.
// Only whole-run completion or an authoritative snapshot releases busy.
export function sessionBusy(busy: boolean, ev: WireEvent): boolean {
  switch (ev.type) {
    case 'turn_start': return true
    case 'done':
    case 'error': return false
    case 'snapshot': return ev.snapshot ? !!ev.snapshot.busy : busy
    default: return busy
  }
}

export function changesRunState(ev: WireEvent): boolean {
  return ev.type === 'turn_start' || ev.type === 'done' || ev.type === 'error' || (ev.type === 'snapshot' && !!ev.snapshot)
}
