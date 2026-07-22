// Track the safe-area insets into the --safe-* CSS variables (declared in
// ui/ui.css) so a top bar's notch padding survives a fixed overlay AND a cold
// standalone (installed-PWA) launch.
//
// The original bug this file fixes: iOS Safari leaves env(safe-area-inset-*)
// reading 0 after a fixed, full-viewport overlay (the sessions drawer, a card
// sheet) is opened and then dismissed. A top bar padded with raw env() then
// collapses onto the notch, into the status-bar touch-exclusion zone where taps
// are swallowed. So we read env() off a probe and write pixel values into the
// --safe-* variables; consumers read var(--safe-*), so a later transient env()=0
// never reaches them.
//
// But freezing the FIRST reading broke two more cases:
//   - A cold installed-PWA (standalone) launch: env(safe-area-inset-top) reads 0
//     for the first frames, so the header froze UNDER the dynamic island and
//     never recovered (Safari, which resolves the inset at load, was fine —
//     hence "works in the browser, broken in the installed app").
//   - Mobile Safari's bottom: env(safe-area-inset-bottom) is ~0 while the bottom
//     toolbar is showing and only grows once it collapses, so a bar frozen at
//     load stayed cramped against the toolbar.
//
// The fix is a running MAXIMUM, not a one-shot freeze: an inset only ever GROWS
// here (within an orientation). That still defeats the dismissed-overlay storm —
// a transient 0 is below the max, so it is ignored and no bar shrinks — while a
// later-arriving true inset (a cold launch settling, a Safari toolbar collapse)
// still raises it to the correct value. Because every write is max-guarded, we
// can now listen for `resize` too (the very event the old freeze avoided, since
// re-measuring during the stale window can only read a smaller value the max
// discards). A genuine geometry change — a rotation — resets the baseline so each
// orientation climbs to its own maximum.

const SIDES = ['top', 'right', 'bottom', 'left'] as const
type Side = (typeof SIDES)[number]

export function trackSafeArea(): void {
  if (typeof document === 'undefined' || !document.body) return

  // A zero-box, invisible, non-interactive probe whose padding is the raw env().
  const probe = document.createElement('div')
  probe.setAttribute('aria-hidden', 'true')
  probe.style.cssText =
    'position:fixed;top:0;left:0;width:0;height:0;visibility:hidden;pointer-events:none;' +
    'padding:env(safe-area-inset-top) env(safe-area-inset-right) env(safe-area-inset-bottom) env(safe-area-inset-left)'
  document.body.appendChild(probe)

  const root = document.documentElement
  const frozen: Record<Side, number> = { top: 0, right: 0, bottom: 0, left: 0 }

  // Read the probe and raise each --safe-* to the largest inset seen so far. It
  // never lowers, so a transient env()=0 (a dismissed overlay) cannot shrink a
  // bar, but a true inset arriving late still lifts it into place.
  const measure = () => {
    const cs = getComputedStyle(probe)
    const px: Record<Side, string> = {
      top: cs.paddingTop,
      right: cs.paddingRight,
      bottom: cs.paddingBottom,
      left: cs.paddingLeft,
    }
    for (const side of SIDES) {
      const v = parseFloat(px[side]) || 0
      if (v > frozen[side]) {
        frozen[side] = v
        root.style.setProperty(`--safe-${side}`, `${v}px`)
      }
    }
  }

  measure()
  // In case trackSafeArea() ran before the first layout resolved the insets.
  requestAnimationFrame(measure)
  // A cold standalone launch reports 0 for the first frames; sample for a couple
  // of seconds so the max settles to the real inset. Every read is max-guarded,
  // so these cannot introduce the shrink jitter the freeze existed to avoid.
  for (const ms of [50, 150, 350, 700, 1200, 2000]) window.setTimeout(measure, ms)
  // Resize covers the keyboard, the Safari toolbar, and the standalone viewport
  // settling after launch; visibilitychange covers a resumed backgrounded PWA.
  window.addEventListener('resize', measure)
  document.addEventListener('visibilitychange', () => {
    if (!document.hidden) measure()
  })
  // A rotation is a real geometry change: the previous orientation's maxima do
  // not carry over (portrait's tall top inset is landscape's side insets), so
  // reset and let the new orientation climb from scratch — after a beat for iOS
  // to settle the new insets, then a few more samples as it does.
  window.addEventListener('orientationchange', () => {
    window.setTimeout(() => {
      for (const side of SIDES) frozen[side] = 0
      measure()
      for (const ms of [150, 400, 800]) window.setTimeout(measure, ms)
    }, 250)
  })
}
