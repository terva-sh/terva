// Freeze the safe-area insets into the --safe-* CSS variables (declared in
// ui/ui.css) so a top bar's notch padding survives a fixed overlay.
//
// The bug: iOS Safari can leave env(safe-area-inset-*) reading 0 after a fixed,
// full-viewport overlay (the sessions drawer, a card sheet) is opened and then
// dismissed. A top bar padded with env() then collapses onto the notch, into the
// status-bar touch-exclusion zone where taps are swallowed. The value only comes
// back on the next unrelated layout — long after the user has given up.
//
// The fix: measure the insets from a probe element while env() is still correct
// (at load, and after a genuine rotation) and write the pixel values into the
// --safe-* variables. Consumers read var(--safe-*), so a later transient env()=0
// never reaches them. We deliberately DO NOT listen for `resize` — that fires
// during exactly the stale window a dismissed overlay opens, and re-measuring
// then would overwrite a good inset with the transient 0 we exist to avoid.
// `orientationchange` is a real inset change, so it is the one re-measure.

const SIDES = ['top', 'right', 'bottom', 'left'] as const

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
  const measure = () => {
    const cs = getComputedStyle(probe)
    const px: Record<(typeof SIDES)[number], string> = {
      top: cs.paddingTop,
      right: cs.paddingRight,
      bottom: cs.paddingBottom,
      left: cs.paddingLeft,
    }
    for (const side of SIDES) {
      root.style.setProperty(`--safe-${side}`, `${parseFloat(px[side]) || 0}px`)
    }
  }

  measure()
  // In case trackSafeArea() ran before the first layout resolved the insets.
  requestAnimationFrame(measure)
  // A rotation genuinely changes the insets; give iOS a beat to settle first.
  window.addEventListener('orientationchange', () => window.setTimeout(measure, 300))
}
