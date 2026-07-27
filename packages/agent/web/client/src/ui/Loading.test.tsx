// @vitest-environment happy-dom
import { afterEach, describe, expect, it, vi } from 'vitest'
import { cleanup, render, screen } from '@testing-library/preact'
import { act } from 'preact/test-utils'
import { ConnectionBanner, Placeholder } from './Loading'

afterEach(() => {
  cleanup()
  vi.useRealTimers()
})

describe('Placeholder', () => {
  // The whole point of the component: a screen reader must be able to tell
  // "still loading" from "loaded, and empty" — the same distinction the shimmer
  // gives a sighted user. Without aria-busy the two are identical announcements.
  it('announces itself as a busy status region', () => {
    render(<Placeholder label="Loading sessions…" />)
    const region = screen.getByRole('status')
    expect(region.getAttribute('aria-busy')).toBe('true')
    expect(region.getAttribute('aria-live')).toBe('polite')
    expect(screen.getByText('Loading sessions…')).toBeTruthy()
  })
})

describe('ConnectionBanner', () => {
  it('says nothing at all once the socket is open', () => {
    render(<ConnectionBanner status="open" />)
    expect(screen.queryByRole('status')).toBeNull()
  })

  // A loopback daemon connects in tens of milliseconds. A banner that appears
  // and vanishes inside that window reads as a fault, not as progress — so the
  // FIRST connect gets a grace period and normally never shows anything.
  it('holds its tongue during the grace period of a first connect', () => {
    vi.useFakeTimers()
    render(<ConnectionBanner status="connecting" graceMs={800} />)
    expect(screen.queryByRole('status')).toBeNull()

    act(() => {
      vi.advanceTimersByTime(799)
    })
    expect(screen.queryByRole('status')).toBeNull()

    act(() => {
      vi.advanceTimersByTime(1)
    })
    expect(screen.getByText('Connecting to terva — nothing here has loaded yet.')).toBeTruthy()
  })

  // Once we have had a live connection, a drop is a different event: the user
  // was reading data that has silently stopped moving. No grace period.
  it('speaks immediately when a live connection drops, and says so differently', () => {
    vi.useFakeTimers()
    const { rerender } = render(<ConnectionBanner status="open" graceMs={800} />)
    rerender(<ConnectionBanner status="closed" graceMs={800} />)
    expect(screen.getByText('Lost the connection to terva — reconnecting…')).toBeTruthy()
  })

  // A reconnect loop alternates closed → connecting → closed every 1.5s. Keying
  // the banner on `status` itself would tear it down and rebuild it on each hop,
  // strobing a message while nothing about the user's situation changed.
  it('stays put across the closed/connecting flapping of a reconnect loop', () => {
    vi.useFakeTimers()
    const { rerender } = render(<ConnectionBanner status="open" graceMs={800} />)
    rerender(<ConnectionBanner status="closed" graceMs={800} />)
    const first = screen.getByRole('status')

    rerender(<ConnectionBanner status="connecting" graceMs={800} />)
    expect(screen.getByRole('status')).toBe(first)

    rerender(<ConnectionBanner status="closed" graceMs={800} />)
    expect(screen.getByRole('status')).toBe(first)
  })

  it('clears the moment the connection comes back', () => {
    vi.useFakeTimers()
    const { rerender } = render(<ConnectionBanner status="closed" graceMs={0} />)
    act(() => {
      vi.advanceTimersByTime(1)
    })
    expect(screen.getByRole('status')).toBeTruthy()

    rerender(<ConnectionBanner status="open" graceMs={0} />)
    expect(screen.queryByRole('status')).toBeNull()
  })
})
