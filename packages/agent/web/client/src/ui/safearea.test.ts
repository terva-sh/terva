// @vitest-environment happy-dom
import { afterEach, describe, expect, it, vi } from 'vitest'
import { trackSafeArea } from './safearea'

// The freeze used to capture the FIRST env() reading, which broke two real cases:
// a cold installed-PWA launch (top inset reads 0 for the first frames) and mobile
// Safari's bottom (inset ~0 until the toolbar collapses). The fix is a running
// maximum — insets only ever grow — which still ignores the transient env()=0 a
// dismissed overlay produces (the reason the freeze existed at all). These pin
// both directions: it climbs to a late inset, and it never lowers.
describe('trackSafeArea running maximum', () => {
  const root = document.documentElement
  // What the probe's getComputedStyle currently reports; the test moves it.
  const cur = { top: '0px', bottom: '0px' }

  afterEach(() => {
    vi.useRealTimers()
    vi.restoreAllMocks()
    document.body.innerHTML = ''
    for (const s of ['top', 'right', 'bottom', 'left']) root.style.removeProperty(`--safe-${s}`)
    cur.top = '0px'
    cur.bottom = '0px'
  })

  function mount() {
    vi.useFakeTimers()
    vi.spyOn(window, 'getComputedStyle').mockImplementation(
      () =>
        ({
          paddingTop: cur.top,
          paddingRight: '0px',
          paddingBottom: cur.bottom,
          paddingLeft: '0px',
        }) as CSSStyleDeclaration,
    )
    trackSafeArea()
  }

  it('raises the top inset to a value that only arrives after launch (cold PWA)', () => {
    mount()
    // At load the standalone viewport reports 0. A 0 is never written — the CSS
    // seed (env(), also 0) stands — so the JS var is untouched, not "0px".
    expect(root.style.getPropertyValue('--safe-top')).toBe('')
    // The real island inset appears a few frames later; the sampling schedule
    // must catch it and lift the var into place.
    cur.top = '59px'
    vi.advanceTimersByTime(2000)
    expect(root.style.getPropertyValue('--safe-top')).toBe('59px')
  })

  it('never lowers an inset when env() transiently reads 0 (a dismissed overlay)', () => {
    mount()
    cur.top = '59px'
    vi.advanceTimersByTime(2000)
    expect(root.style.getPropertyValue('--safe-top')).toBe('59px')
    // A fixed overlay dismiss drops env() to 0 and fires a resize — the exact
    // storm the freeze guards. The max must hold.
    cur.top = '0px'
    window.dispatchEvent(new Event('resize'))
    expect(root.style.getPropertyValue('--safe-top')).toBe('59px')
  })

  it('grows the bottom inset when Safari reveals it (toolbar collapse)', () => {
    mount()
    // Toolbar up: ~0, so nothing is written yet (the CSS seed stands). It grows
    // to the home-indicator height as the toolbar collapses (a resize) — the
    // composer clearance must follow it up.
    expect(root.style.getPropertyValue('--safe-bottom')).toBe('')
    cur.bottom = '34px'
    window.dispatchEvent(new Event('resize'))
    expect(root.style.getPropertyValue('--safe-bottom')).toBe('34px')
  })
})
