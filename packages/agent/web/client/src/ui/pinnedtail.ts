// Follow a growing feed's tail without stealing the scroll from a reader.
//
// Three surfaces stream content into a scrolling box — the panel transcript,
// Stage's chat, and the RAATI deliberation ticker — and all three want the same
// thing: land at the bottom, follow new content, and STOP following the moment
// the reader scrolls up to read something. Two of them had implemented that,
// separately and almost identically. The third (the ticker) had only the
// follow half, so it yanked the view back to the newest line on every event of
// a deliberation, which is exactly when there is something worth reading.
//
// The missing half is the pin check, and it is the whole reason this is shared
// rather than four lines at each call site: `el.scrollTop = el.scrollHeight` is
// a complete-looking statement that is wrong on its own, and nothing about it
// invites the question "what if they scrolled up".

import { useCallback, useLayoutEffect, useRef, useState } from 'preact/hooks'
import type { Ref } from 'preact'

// How close to the bottom still counts as "at the end". 80px is roughly a line
// or two of a transcript — enough that a trackpad's overscroll settle or a
// re-layout does not unpin you, small enough that a deliberate scroll up does.
//
// It is capped at half the visible height because the same absolute number is
// nonsense in a short pane: the RAATI ticker is 74px tall, so a flat 80px would
// call every position "the end" and the pin check would do nothing at all. On
// the tall panes (hundreds of px) the cap never binds and the threshold is the
// 80px both transcripts already shipped.
const PIN_SLACK_PX = 80

function pinSlack(el: HTMLElement): number {
  return Math.min(PIN_SLACK_PX, el.clientHeight / 2)
}

export interface PinnedTail<T extends HTMLElement> {
  // Attach to the SCROLLING element (the one with overflow), not its wrapper.
  ref: Ref<T>
  // Wire to that element's onScroll — this is what unpins.
  onScroll: () => void
  // True once the reader has scrolled off the end: the cue for a "jump to
  // latest" affordance. A pane too small to hold one can ignore it.
  showJump: boolean
  // Re-pin and return to the end. Safe to call when already pinned.
  jumpToLatest: () => void
}

// usePinnedTail returns the wiring for one such box.
//
//   deps    — re-run the follow when these change: every input that can grow
//             the content (items, a busy spinner, a queued-message strip).
//   repinOn — a value whose change means "this is a different feed now":
//             re-pin unconditionally and drop the jump cue. Stage passes the
//             session id, so opening another chat starts at its end even if
//             you had scrolled up in the previous one.
//
// The follow is a LAYOUT effect: the scroll lands before paint, so there is no
// frame showing the old position. Both re-pin paths are layout effects too, and
// the re-pin is declared first so that on a commit where the feed changed AND
// its content did, the re-pin is already in effect when the follow reads it.
export function usePinnedTail<T extends HTMLElement>(deps: unknown[], repinOn?: unknown): PinnedTail<T> {
  const ref = useRef<T>(null)
  // A ref, not state: the follow effect must read the value written by the
  // scroll handler in the same tick, and it must not itself cause a render.
  const pinned = useRef(true)
  const [showJump, setShowJump] = useState(false)

  useLayoutEffect(() => {
    pinned.current = true
    setShowJump(false)
  }, [repinOn])

  useLayoutEffect(() => {
    const el = ref.current
    if (el && pinned.current) el.scrollTop = el.scrollHeight
  }, deps)

  const onScroll = useCallback(() => {
    const el = ref.current
    if (!el) return
    const nearBottom = el.scrollHeight - el.scrollTop - el.clientHeight < pinSlack(el)
    pinned.current = nearBottom
    setShowJump(!nearBottom)
  }, [])

  const jumpToLatest = useCallback(() => {
    const el = ref.current
    if (!el) return
    el.scrollTop = el.scrollHeight
    pinned.current = true
    setShowJump(false)
  }, [])

  return { ref, onScroll, showJump, jumpToLatest }
}
