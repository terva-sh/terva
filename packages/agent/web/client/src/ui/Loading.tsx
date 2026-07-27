import { useEffect, useState } from 'preact/hooks'
import { t } from '../i18n'

// The boot-state primitives, shared by the panel and Stage.
//
// Why they exist: a list has THREE states, not two — it has rows, it asked and
// there are none, or it has not asked yet. Every surface here collapsed the
// third into the second, so a page that had merely finished painting told you
// "No sessions in this workspace yet." and "No personas available." while the
// socket was still connecting. Those are not placeholders; they are claims, and
// they were false. The connection dot in the corner did say `connecting`, but a
// 9px circle does not retract a sentence — and being colour-only it said nothing
// at all to a colourblind or screen-reader user.
//
// So the fix is not "add a spinner beside the lie". It is: nothing asserts
// emptiness before its first answer, and the wait says so out loud.
//
// Both live in ui/ (and are styled in ui.css) because the bug is identical on
// both surfaces and a rule that lives in one app's sheet is exactly the drift
// ui-conformance.test.ts was written to stop.

// Placeholder stands in for a list that has not been answered yet.
//
// role=status + aria-live so the wait is ANNOUNCED rather than being a purely
// visual shimmer, and aria-busy so assistive tech knows the region is unsettled
// rather than finished-and-empty — the same distinction the sighted user gets.
export function Placeholder({ label, rows = 3 }: { label?: string; rows?: number }) {
  return (
    <div class="ui-placeholder" role="status" aria-live="polite" aria-busy="true">
      <span class="ui-placeholder__label">{label || t('Loading…')}</span>
      {Array.from({ length: rows }, (_, i) => (
        <span key={i} class="ui-placeholder__row" aria-hidden="true" />
      ))}
    </div>
  )
}

// How long a FIRST connect may take before the banner appears. A loopback daemon
// answers in tens of milliseconds; a banner that appears and vanishes inside two
// frames reads as a fault rather than as progress. A drop AFTER we have been
// open skips the grace entirely — see below.
const GRACE_MS = 800

// ConnectionBanner is the loud half: an in-flow strip under the top bar whenever
// the socket is not open.
//
// It deliberately does not block. The screen behind it stays readable and
// scrollable, because during a 1.5s reconnect blip there is nothing wrong with
// what is already on it — the only thing wrong was believing it was current.
//
// `status` is the ctrlproto Status ('connecting' | 'open' | 'closed'), typed as
// a plain string so ui/ does not depend on the wire types.
export function ConnectionBanner({ status, graceMs = GRACE_MS }: { status: string; graceMs?: number }) {
  const [show, setShow] = useState(false)
  // Whether this page has ever had a live connection. It picks BOTH the wording
  // and the urgency: before the first hello this is boot, which is usually
  // instant and not worth interrupting for; after it, the user was reading live
  // data a moment ago and it has silently stopped moving, which is worth saying
  // at once.
  const [everOpen, setEverOpen] = useState(false)
  // One boolean for both down states on purpose. A reconnect loop alternates
  // closed → connecting → closed every 1.5s, so keying the banner on `status`
  // itself would strobe it in and out of existence while nothing about the
  // user's situation changed.
  const down = status !== 'open'

  useEffect(() => {
    if (!down) {
      setEverOpen(true)
      setShow(false)
      return
    }
    if (everOpen) {
      setShow(true)
      return
    }
    const id = setTimeout(() => setShow(true), graceMs)
    return () => clearTimeout(id)
  }, [down, everOpen, graceMs])

  if (!show) return null
  return (
    <div class={`ui-conn-banner${everOpen ? ' ui-conn-banner--lost' : ''}`} role="status" aria-live="polite">
      <span class="ui-conn-banner__spinner" aria-hidden="true" />
      <span class="ui-conn-banner__text">
        {/* Surface-neutral on purpose: this component renders on the panel,
            where the things being waited for are "sessions", and on Stage,
            where the same things are "chats". Naming either one puts the other
            app's vocabulary on screen. */}
        {everOpen
          ? t('Lost the connection to terva — reconnecting…')
          : t('Connecting to terva — nothing here has loaded yet.')}
      </span>
    </div>
  )
}
