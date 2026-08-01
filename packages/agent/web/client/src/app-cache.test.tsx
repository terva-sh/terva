// @vitest-environment happy-dom
import { afterEach, describe, expect, it } from 'vitest'
import { cleanup, render } from '@testing-library/preact'

import { CacheSummary, hitRate } from './app'
import type { ContextCache, WireUsage } from './platform/ctrlproto/types'

// The prompt-cache panel.
//
// What it is for: every other row of the usage pane treats a 180k context the
// same way whether it cost pennies or dollars. The difference is entirely the
// prefix cache, and nothing surfaced it — the pane showed a raw cumulative
// cache_read count with no denominator, which is a number nobody can act on.

const usage = (over: Partial<WireUsage> = {}): WireUsage => ({
  input: 0,
  output: 0,
  cache_read: 0,
  cache_write: 0,
  cost_usd: 0,
  ...over,
})

const cache = (over: Partial<ContextCache> = {}): ContextCache => ({
  supported: true,
  last_request: usage(),
  session: usage(),
  ...over,
})

afterEach(cleanup)

describe('hitRate', () => {
  it('divides cache reads by the whole prompt, cached and not', () => {
    expect(hitRate(usage({ input: 1_000, cache_read: 3_000 }))).toBe(0.75)
    expect(hitRate(usage({ input: 1_000, cache_read: 3_000, cache_write: 1_000 }))).toBeCloseTo(0.6)
  })

  // Output is not prompt. Counting it would deflate every rate by however much
  // the model happened to say.
  it('ignores output tokens', () => {
    expect(hitRate(usage({ input: 1_000, cache_read: 1_000, output: 50_000 }))).toBe(0.5)
  })

  // "Nothing has been asked of the cache" and "the cache missed" are opposite
  // facts with the same numerator. null is how the caller tells them apart.
  it('returns null rather than 0 when there is no prompt at all', () => {
    expect(hitRate(usage())).toBeNull()
    expect(hitRate(usage({ output: 20 }))).toBeNull()
    expect(hitRate(usage({ input: 10 }))).toBe(0)
  })
})

describe('CacheSummary', () => {
  // A daemon older than this field sends nothing. An empty render would report
  // a perfectly good cache as dead.
  it('renders nothing when the server sent no cache field', () => {
    const { container } = render(<CacheSummary cache={undefined} />)
    expect(container.querySelector('.ctx-cache')).toBeNull()
  })

  it('separates "no traffic yet" from "this provider has no cache"', () => {
    const fresh = render(<CacheSummary cache={cache({ supported: false })} />)
    expect(fresh.container.textContent).toContain('no requests yet')
    cleanup()

    const uncached = render(
      <CacheSummary cache={cache({ supported: false, session: usage({ input: 40_000 }) })} />,
    )
    expect(uncached.container.textContent).toContain('no cache activity')
    // Emphatically not a 0% hit rate: there is no cache here to be missing, and
    // "0%" sends someone hunting a misconfiguration that does not exist.
    expect(uncached.container.textContent).not.toContain('0%')
    expect(uncached.container.querySelector('.ctx-bar-fill')).toBeNull()
  })

  it('fills the bar to the session hit rate', () => {
    const { container } = render(
      <CacheSummary cache={cache({ session: usage({ input: 10_000, cache_read: 190_000 }) })} />,
    )
    const fill = container.querySelector('.ctx-bar-fill') as HTMLElement
    expect(fill.style.width).toBe('95%')
    // Polarity: a FULL bar is the good state here, unlike the context gauge
    // directly above it, so the alarm colour must not be on at 95%.
    expect(fill.className).not.toContain('hot')
    expect(container.textContent).toContain('95%')
  })

  it('flags a low hit rate at the empty end of the bar', () => {
    const { container } = render(
      <CacheSummary cache={cache({ session: usage({ input: 90_000, cache_read: 10_000 }) })} />,
    )
    expect((container.querySelector('.ctx-bar-fill') as HTMLElement).className).toContain('hot')
  })

  it('breaks the last request into read, written and full-price', () => {
    const { container } = render(
      <CacheSummary
        cache={cache({
          session: usage({ input: 10_000, cache_read: 190_000 }),
          last_request: usage({ input: 2_000, cache_read: 180_000, cache_write: 1_500 }),
        })}
      />,
    )
    const text = container.textContent ?? ''
    expect(text).toContain('180k')
    expect(text).toContain('2k')
  })

  // The finding this panel exists to surface. A prefix that keeps getting
  // rewritten and never read back costs 25% MORE than no cache at all, and
  // "saved -$0.42" is a phrase people read as a saving.
  it('says a negative saving cost money, in different words', () => {
    const { container } = render(
      <CacheSummary
        cache={cache({
          session: usage({ input: 1_000, cache_write: 100_000, cache_saved_usd: -0.42 }),
          last_request: usage({ input: 1_000, cache_write: 100_000 }),
        })}
      />,
    )
    const text = container.textContent ?? ''
    expect(text).toContain('$0.42')
    expect(text).not.toContain('saved')
    expect(container.querySelector('.ctx-usage-cost.bad')).not.toBeNull()
  })

  it('shows a positive saving as saved', () => {
    const { container } = render(
      <CacheSummary
        cache={cache({ session: usage({ input: 1_000, cache_read: 99_000, cache_saved_usd: 0.486 }) })}
      />,
    )
    expect(container.textContent).toContain('saved $0.49')
    expect(container.querySelector('.ctx-usage-cost.bad')).toBeNull()
  })

  describe('the per-request strip', () => {
    // One sample is not a shape, and a lone bar invites reading its height as a
    // value — the one thing the strip does not mean.
    it('needs more than one request before it draws', () => {
      const { container } = render(
        <CacheSummary
          cache={cache({
            session: usage({ input: 1_000, cache_read: 9_000 }),
            recent: [{ hit_rate: 0.9, prompt_tokens: 10_000 }],
          })}
        />,
      )
      expect(container.querySelector('.ctx-cache-strip')).toBeNull()
    })

    it('draws one bar per request, oldest first, and marks where the cache broke', () => {
      const { container } = render(
        <CacheSummary
          cache={cache({
            session: usage({ input: 10_000, cache_read: 90_000 }),
            recent: [
              { hit_rate: 1, prompt_tokens: 100_000 },
              { hit_rate: 0, prompt_tokens: 100_000 }, // the prefix changed here
              { hit_rate: 1, prompt_tokens: 100_000 },
            ],
          })}
        />,
      )
      const fills = [...container.querySelectorAll('.ctx-cache-fill')] as HTMLElement[]
      expect(fills).toHaveLength(3)
      expect(fills[0].style.height).toBe('100%')
      expect(fills[2].style.height).toBe('100%')
      // The markup carries the honest height; keeping a total miss visible is
      // .ctx-cache-fill's min-height, so this must NOT be padded up.
      expect(fills[1].style.height).toBe('0%')
      expect(fills[1].className).toContain('hot')
      expect(fills[0].className).not.toContain('hot')
    })

    it('clamps a rate outside [0,1] instead of drawing past the row', () => {
      const { container } = render(
        <CacheSummary
          cache={cache({
            session: usage({ input: 1, cache_read: 9 }),
            recent: [
              { hit_rate: 1.4, prompt_tokens: 10 },
              { hit_rate: -0.3, prompt_tokens: 10 },
            ],
          })}
        />,
      )
      const fills = [...container.querySelectorAll('.ctx-cache-fill')] as HTMLElement[]
      expect(fills[0].style.height).toBe('100%')
      expect(fills[1].style.height).toBe('0%')
    })
  })
})
