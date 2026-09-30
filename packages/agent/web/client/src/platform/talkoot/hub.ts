import { ADDR_TALKOOT_PREFIX, type WireEvent } from '../ctrlproto/types'

// TalkootHub hands the events of a talkoot's address to whoever listens for
// that talkoot.
//
// 🔑 The panel owns the client's single onEvent: it routes the workspace and
// the focused session. A view that swapped onEvent for its own, as Stage does,
// would starve the panel while it is open. So the panel forwards each
// #talkoot: address here, and a view listens.
export class TalkootHub {
  private listeners = new Map<string, Set<(ev: WireEvent) => void>>()
  private lists = new Set<() => void>()

  // onList registers fn for talkoots_changed, which says the talkoot list
  // changed, and returns its removal.
  onList(fn: () => void): () => void {
    this.lists.add(fn)
    return () => {
      this.lists.delete(fn)
    }
  }

  // listen registers fn for one talkoot's events, and returns its removal.
  listen(id: string, fn: (ev: WireEvent) => void): () => void {
    let set = this.listeners.get(id)
    if (!set) {
      set = new Set()
      this.listeners.set(id, set)
    }
    set.add(fn)
    return () => {
      set.delete(fn)
      if (set.size === 0) this.listeners.delete(id)
    }
  }

  // dispatch delivers ev when addr is a talkoot address, and reports whether
  // it was one, so the caller stops routing it.
  // talkoots_changed rides #workspace, and it goes to the list listeners.
  dispatch(addr: string, ev: WireEvent): boolean {
    if (ev.type === 'talkoots_changed') {
      for (const fn of [...this.lists]) fn()
      return true
    }
    if (!addr.startsWith(ADDR_TALKOOT_PREFIX)) return false
    const set = this.listeners.get(addr.slice(ADDR_TALKOOT_PREFIX.length))
    if (set) for (const fn of [...set]) fn(ev)
    return true
  }
}
