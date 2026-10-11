import { useCallback, useEffect, useRef, useState } from 'preact/hooks'
import type { ClientLike } from '../../platform/ctrlproto/client'
import {
  talkootAddr,
  type TalkootInboxResult,
  type TalkootRoomPage,
  type TalkootView,
  type WireEvent,
} from '../../platform/ctrlproto/types'
import type { TalkootHub } from '../../platform/talkoot/hub'
import {
  applyTalkootEvent,
  emptyTalkoot,
  mergePage,
  rosterStale,
  setCards,
  setView,
  type TalkootState,
} from '../../platform/talkoot/store'

// PAGE is how many room lines one read takes.
const PAGE = 100

// How long a failed load or roster refresh waits before it tries again.
export const REFRESH_RETRY_MS = 5000

// A failed load waits longer after each failure, up to this. A team that
// stopped answers every load with an error, and the view keeps trying at
// this pace until the person picks another team.
export const LOAD_RETRY_MAX_MS = 60_000

// useTalkoot holds one talkoot: it reads the roster, the newest room page, and
// the inbox, and folds the talkoot's events into them.
//
// `generation` counts connections and must change on every reconnect. A
// subscription lives on one socket, so after a reconnect this subscribes
// again and reads the room again. The page merge drops the lines it already
// holds, so a reconnect loses no line and repeats none.
export function useTalkoot(client: ClientLike, hub: TalkootHub, id: string, generation: number) {
  const [state, setState] = useState<TalkootState>(() => emptyTalkoot(id))
  const [error, setError] = useState('')
  const ref = useRef(state)
  const update = useCallback((fn: (s: TalkootState) => TalkootState) => {
    const next = fn(ref.current)
    if (next === ref.current) return
    ref.current = next
    setState(next)
  }, [])

  // A new talkoot starts from nothing.
  useEffect(() => {
    ref.current = emptyTalkoot(id)
    setState(ref.current)
    setError('')
    loadFailures.current = 0
  }, [id])

  // Counts loads. A refresh that a load overtook is dropped: it read the
  // roster on the old socket, and events missed while that socket was down
  // never raised its generation, so the generation alone cannot tell.
  const loads = useRef(0)
  // Counts retries of a failed load, and the failures since the last load
  // that succeeded.
  const [loadRetry, setLoadRetry] = useState(0)
  const loadFailures = useRef(0)

  const refreshView = useCallback(async () => {
    const gen = ref.current.rosterGen
    const load = loads.current
    const v = await client.send<TalkootView>('talkoot.get', { id })
    if (load !== loads.current) return
    update((s) => setView(s, v, gen, true))
    setError('')
  }, [client, id, update])

  useEffect(() => {
    if (!id || generation <= 0) return
    let live = true
    let timer: ReturnType<typeof setTimeout> | undefined
    loads.current++
    // 🚨 Events that arrive while the reads are in flight wait here, and are
    // applied on top of the snapshot once it lands. Applied first, they
    // would be overwritten: the inbox read replaces every card, and the
    // roster read replaces every member's status. Replaying them is safe,
    // because each event is keyed: a line is deduplicated, a card replaced
    // or removed by key, and a status replaced.
    let pending: WireEvent[] | null = []
    // Listen before subscribing, so no event falls between the two.
    const stop = hub.listen(id, (ev: WireEvent) => {
      if (!live) return
      if (pending) pending.push(ev)
      else update((s) => applyTalkootEvent(s, ev))
    })
    client.fire('subscribe', null, talkootAddr(id))
    Promise.all([
      client.send<TalkootView>('talkoot.get', { id }),
      client.send<TalkootRoomPage>('talkoot.room', { id, limit: PAGE }),
      client.send<TalkootInboxResult>('talkoot.inbox', { id }),
    ]).then(
      ([v, page, inbox]) => {
        if (!live) return
        const held = pending ?? []
        pending = null
        update((s) => held.reduce(applyTalkootEvent, setCards(mergePage(setView(s, v), page, false), inbox.cards)))
        loadFailures.current = 0
        setError('')
      },
      (e: unknown) => {
        if (!live) return
        // Without a snapshot, the events that arrived are still news.
        const held = pending ?? []
        pending = null
        update((s) => held.reduce(applyTalkootEvent, s))
        setError(e instanceof Error ? e.message : String(e))
        // The socket may stay up, so no reconnect comes to load again.
        const wait = Math.min(REFRESH_RETRY_MS * 2 ** loadFailures.current, LOAD_RETRY_MAX_MS)
        loadFailures.current++
        timer = setTimeout(() => setLoadRetry((n) => n + 1), wait)
      },
    )
    return () => {
      live = false
      clearTimeout(timer)
      stop()
      client.fire('unsubscribe', null, talkootAddr(id))
    }
  }, [client, hub, id, generation, loadRetry, update])

  // A roster line makes the member list stale: read it again. A failed read
  // tries again after a pause, because no other event may come to trigger it.
  const stale = rosterStale(state)
  const [retry, setRetry] = useState(0)
  useEffect(() => {
    if (!stale) return
    let live = true
    let timer: ReturnType<typeof setTimeout> | undefined
    refreshView().catch((e: unknown) => {
      if (!live) return
      setError(e instanceof Error ? e.message : String(e))
      timer = setTimeout(() => setRetry((n) => n + 1), REFRESH_RETRY_MS)
    })
    return () => {
      live = false
      clearTimeout(timer)
    }
  }, [stale, state.rosterGen, refreshView, retry])

  const loadOlder = useCallback(async () => {
    const before = ref.current.next
    if (!before) return
    const load = loads.current
    try {
      const page = await client.send<TalkootRoomPage>('talkoot.room', { id, before, limit: PAGE })
      // 🚨 A page read before a reconnect's snapshot would put back the gap
      // that the snapshot closed, and a second click must not add one twice.
      if (load !== loads.current || ref.current.next !== before) return
      update((s) => mergePage(s, page, true))
      setError('')
    } catch (e: unknown) {
      if (load !== loads.current) return
      setError(e instanceof Error ? e.message : String(e))
    }
  }, [client, id, update])

  return { state, error, loadOlder, refreshView }
}
