// @vitest-environment happy-dom
import { describe, expect, it, beforeEach } from 'vitest'
import { panelHref, stageHref, takeNavParams } from './navlinks'

function at(url: string) {
  history.replaceState(null, '', url)
}

describe('panelHref / stageHref', () => {
  it('links bare when there is nothing to carry', () => {
    expect(panelHref()).toBe('/')
    expect(stageHref()).toBe('/stage/')
    // An empty id must not produce a dangling `?session=`, because the reader
    // would then strip a param that said nothing.
    expect(panelHref('')).toBe('/')
    expect(stageHref('')).toBe('/stage/')
  })

  it('carries the session, and the pane only alongside it', () => {
    expect(panelHref('20260720-182451-27cbde4a')).toBe('/?session=20260720-182451-27cbde4a')
    expect(panelHref('20260720-182451-27cbde4a', 'context')).toBe(
      '/?session=20260720-182451-27cbde4a&pane=context',
    )
    expect(stageHref('20260720-182451-27cbde4a')).toBe('/stage/?session=20260720-182451-27cbde4a')
  })

  it('never carries a token', () => {
    // Auth rides the cookie the daemon mints; copying tokens into cross-app
    // links would only widen where they can leak.
    at('/?token=secret')
    expect(panelHref('20260720-182451-27cbde4a')).not.toContain('token')
    expect(stageHref('20260720-182451-27cbde4a')).not.toContain('token')
  })
})

describe('takeNavParams', () => {
  beforeEach(() => at('/'))

  it('reads a well-formed session and pane', () => {
    at('/?session=20260720-182451-27cbde4a&pane=context')
    expect(takeNavParams()).toEqual({ session: '20260720-182451-27cbde4a', pane: 'context' })
  })

  it('strips the params it consumed', () => {
    at('/?session=20260720-182451-27cbde4a&pane=context')
    takeNavParams()
    expect(location.search).toBe('')
  })

  it('leaves unrelated params alone', () => {
    // ?token= is handled by the client's auth path and must survive.
    at('/?token=abc&session=20260720-182451-27cbde4a')
    expect(takeNavParams().session).toBe('20260720-182451-27cbde4a')
    expect(location.search).toBe('?token=abc')
  })

  it('rejects a session id that is not one', () => {
    // A hand-edited or crafted URL must not reach app state. Anything that is
    // not <date>-<time>-<hex> is dropped rather than subscribed to.
    for (const bad of ['../../etc/passwd', 'not-an-id', '<script>', '20260720-182451-ZZZZZZZZ']) {
      at(`/?session=${encodeURIComponent(bad)}`)
      expect(takeNavParams().session).toBe('')
    }
  })

  it('rejects a pane outside the allowed set', () => {
    at('/?pane=admin')
    expect(takeNavParams().pane).toBe('')
  })

  it('still strips a rejected param', () => {
    // Otherwise a bad value would persist in the URL and be re-read forever.
    at('/?session=nope&pane=admin')
    expect(takeNavParams()).toEqual({ session: '', pane: '' })
    expect(location.search).toBe('')
  })

  it('is a no-op on a clean URL', () => {
    at('/stage/')
    expect(takeNavParams()).toEqual({ session: '', pane: '' })
    expect(location.pathname).toBe('/stage/')
  })
})
