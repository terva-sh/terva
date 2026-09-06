import { useCallback, useEffect, useMemo, useRef, useState } from 'preact/hooks'

// The toast is the panel's one-line interruption: something happened that the
// user should see but does not have to act on. Two things about it are
// deliberate, and both of them were bugs.
//
// PLACEMENT. The toast is anchored to the TOP EDGE of the composer, through a
// --toast-lift variable the composer measures itself into (see Composer.tsx).
// The old rule was `bottom: 80px` — a constant standing in for the composer's
// height, and correct only for an empty single-line input. A multi-line draft,
// a row of attachment chips or a next-step offer each grow the composer upward
// THROUGH that constant, so the message ended up covering the text it was
// talking about. CSS cannot reference a sibling's height, so the height has to
// be measured; the variable is the seam. Where there is no composer at all
// (landing, board) the variable is absent and the safe-area inset is the floor.
//
// LIFETIME. Only an error waits to be dismissed. Everything else — a save
// confirmed, a rejection explained, a usage hint — fades on its own, because a
// message the reader has taken in and cannot act on is from then on just an
// obstruction sitting over their input. Hovering or focusing the toast pauses
// the clock and leaving resumes it with the time that was left, so a long
// sentence never vanishes out from under someone mid-read.

/** The three things a toast can be. Only `error` waits to be dismissed. */
export type ToastKind = 'error' | 'success' | 'note'

export interface Toast {
  text: string
  kind: ToastKind
  /**
   * Bumped once per message. It keys the view, so an identical message shown
   * twice in a row is still a second toast with a fresh clock rather than a
   * silent no-op on an already-expiring one.
   */
  seq: number
}

/**
 * How long a self-dismissing toast stays before it starts to fade. Long,
 * because these carry sentences and not just words, and because the clock is
 * paused while the pointer is on it.
 */
export const TOAST_MS = 15_000

/** The fade itself. Long enough to read as a fade, short enough not to linger. */
export const TOAST_FADE_MS = 220

/**
 * The four ways the panel raises a toast. Call sites say what KIND of thing
 * happened and never how long it should live — the policy is here, in one
 * place, so it stays one policy.
 */
export interface Notify {
  /** Something went wrong. Stays until the user dismisses it. */
  error: (text: string) => void
  /** Something the user asked for succeeded. Fades. */
  ok: (text: string) => void
  /** A hint, a refusal, or work in progress. Fades. */
  note: (text: string) => void
  /** Take down whatever is showing. */
  clear: () => void
  /**
   * Take down a specific message, and only if it is still the one showing.
   * This is for progress notes ("Generating title…") whose completion arrives
   * later: by then a newer toast may have replaced it, and clearing blindly
   * would swallow that newer one.
   */
  clearIf: (text: string) => void
}

export function useToast(): { toast: Toast | null; notify: Notify } {
  const [toast, setToast] = useState<Toast | null>(null)
  const seq = useRef(0)

  const show = useCallback((kind: ToastKind, text: string) => {
    // An empty message is a clear, not a blank toast. Several call sites pass
    // a message that may be empty (a server error with no text), and an empty
    // red box says nothing.
    if (!text) {
      setToast(null)
      return
    }
    seq.current += 1
    setToast({ text, kind, seq: seq.current })
  }, [])

  const notify = useMemo<Notify>(
    () => ({
      error: (text) => show('error', text),
      ok: (text) => show('success', text),
      note: (text) => show('note', text),
      clear: () => setToast(null),
      clearIf: (text) => setToast((cur) => (cur && cur.text === text ? null : cur)),
    }),
    [show],
  )

  return { toast, notify }
}

/**
 * The dock is rendered unconditionally, empty or not, because it is the live
 * region: a screen reader announces changes INSIDE a region that was already
 * there, and a region that appears at the same moment as its content is
 * announced unreliably or not at all. It is `polite` for every kind — an
 * assertive region would have to be a second, separate element, and an error
 * toast here is persistent and visible rather than something that has to
 * interrupt.
 */
export function ToastDock({ toast, onDismiss }: { toast: Toast | null; onDismiss: () => void }) {
  return (
    <div class="toast-dock" role="status" aria-live="polite" aria-atomic="true">
      {toast && <ToastView key={toast.seq} toast={toast} onDismiss={onDismiss} />}
    </div>
  )
}

export function ToastView({ toast, onDismiss }: { toast: Toast; onDismiss: () => void }) {
  const [leaving, setLeaving] = useState(false)
  const [held, setHeld] = useState(false)
  // What is left of the clock. A ref rather than state: the pause has to read
  // it and write it without re-running the effect that owns the timer.
  const left = useRef(TOAST_MS)

  // The clock. Re-armed whenever the hold flips, and the cleanup is what
  // records the time already spent — so a pause banks the elapsed time and the
  // next arm gets only the remainder, rather than restarting the full wait.
  useEffect(() => {
    if (toast.kind === 'error' || held) return
    const armedAt = Date.now()
    const timer = setTimeout(() => setLeaving(true), Math.max(0, left.current))
    return () => {
      clearTimeout(timer)
      left.current -= Date.now() - armedAt
    }
  }, [toast.kind, held])

  // The fade runs on the element; this is only the unmount that follows it.
  useEffect(() => {
    if (!leaving) return
    const timer = setTimeout(onDismiss, TOAST_FADE_MS)
    return () => clearTimeout(timer)
  }, [leaving, onDismiss])

  return (
    <button
      type="button"
      class={`toast toast--${toast.kind}${leaving ? ' is-leaving' : ''}`}
      data-kind={toast.kind}
      onClick={onDismiss}
      onMouseEnter={() => setHeld(true)}
      onMouseLeave={() => setHeld(false)}
      onFocus={() => setHeld(true)}
      onBlur={() => setHeld(false)}
    >
      {toast.text}
    </button>
  )
}
