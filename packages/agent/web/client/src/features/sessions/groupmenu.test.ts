import { describe, expect, it } from 'vitest'
import { nudge } from './GroupMenu'

// The measured numbers below are the real ones from the landing at 1280px: the
// board's scroll column runs 190..1090, and the menu on the leftmost tile lands
// at 152..320 — 38px of it outside, which the container silently cut off.
const LANDING = { left: 190, right: 1090 }

describe('nudge', () => {
  it('leaves a menu that already fits where it is', () => {
    expect(nudge({ left: 793, right: 961 }, LANDING)).toBe(0)
  })

  it('brings a menu that runs off the left edge back inside', () => {
    expect(nudge({ left: 152, right: 320 }, LANDING)).toBe(46)
  })

  it('brings a menu that runs off the right edge back inside', () => {
    expect(nudge({ left: 950, right: 1118 }, LANDING)).toBe(-36)
  })

  it('counts the margin as inside, not as room to spare', () => {
    // Flush against the margin is the last position that needs no shift; one
    // pixel further out is the first that does.
    expect(nudge({ left: 198, right: 366 }, LANDING)).toBe(0)
    expect(nudge({ left: 197, right: 365 }, LANDING)).toBe(1)
  })

  it('pins a menu too wide for its container to the left edge', () => {
    // Both edges are outside and no shift can fix both. Anchoring the left is
    // what keeps the START of each group name readable.
    expect(nudge({ left: 100, right: 1200 }, LANDING)).toBe(98)
  })

  it('falls back to the viewport when nothing above it clips', () => {
    expect(nudge({ left: -10, right: 158 }, { left: 0, right: 390 })).toBe(18)
  })
})
