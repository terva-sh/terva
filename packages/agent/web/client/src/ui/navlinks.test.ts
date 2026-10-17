// @vitest-environment happy-dom
import { describe, expect, it, beforeEach } from 'vitest'
import { panelHref, stageCardHref, stageHref, takeNavParams, takeTalkootParams, talkootHref } from './navlinks'

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
    expect(takeNavParams()).toEqual({ session: '20260720-182451-27cbde4a', pane: 'context', card: '' })
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

  it('names panes by the surface ids the daemon declares', () => {
    // The worktree pane's id is "worktrees". The allowlist once said
    // "worktree", so that link passed the check and opened no pane at all.
    at('/?pane=worktrees')
    expect(takeNavParams().pane).toBe('worktrees')
    at('/?pane=worktree')
    expect(takeNavParams().pane).toBe('')
  })

  it('reads a fleet hub session id, which carries its origin', () => {
    // Behind a hub every session id is <origin>/<id>, the hub's own included
    // ("local/…"). Rejecting the slash broke every session link on a hub.
    for (const good of ['neot/20260720-182451-27cbde4a', 'local/20260720-182451-27cbde4a', 'build-box.lan/20260720-182451-27cbde4a']) {
      at(`/?session=${encodeURIComponent(good)}`)
      expect(takeNavParams().session, good).toBe(good)
    }
  })

  it('rejects an origin that ctrlproto.ValidOrigin would refuse', () => {
    for (const bad of [
      'Neot/20260720-182451-27cbde4a',
      'a/b/20260720-182451-27cbde4a',
      '-neot/20260720-182451-27cbde4a',
      '../20260720-182451-27cbde4a',
      '/20260720-182451-27cbde4a',
      'neot/not-an-id',
    ]) {
      at(`/?session=${encodeURIComponent(bad)}`)
      expect(takeNavParams().session, bad).toBe('')
    }
  })

  it('still strips a rejected param', () => {
    // Otherwise a bad value would persist in the URL and be re-read forever.
    at('/?session=nope&pane=admin')
    expect(takeNavParams()).toEqual({ session: '', pane: '', card: '' })
    expect(location.search).toBe('')
  })

  it('is a no-op on a clean URL', () => {
    at('/stage/')
    expect(takeNavParams()).toEqual({ session: '', pane: '', card: '' })
    expect(location.pathname).toBe('/stage/')
  })
})

// The character deep link: the studio is reachable from outside Stage (the
// panel, a bookmark), and the id is validated before it reaches app state for
// the same reason the session id is — a hand-edited URL must not address the
// card store.
describe('takeNavParams card', () => {
  it('reads a well-formed card id and strips it', () => {
    at('/stage/?card=kobeni-a1b2c3d4e5f6')
    expect(takeNavParams().card).toBe('kobeni-a1b2c3d4e5f6')
    expect(location.search).toBe('')
  })

  it('rejects an id that could escape the card store, and still strips it', () => {
    for (const bad of ['../escape', 'has/slash', 'UPPER', '', 'has space', '-leading']) {
      at('/stage/?card=' + encodeURIComponent(bad))
      expect(takeNavParams().card, bad).toBe('')
    }
  })

  it('builds a studio link', () => {
    expect(stageCardHref('kobeni-a1b2c3d4e5f6')).toBe('/stage/?card=kobeni-a1b2c3d4e5f6')
  })
})

// The Talkoot app's deep link: a team, and one member's thread in it. Both ids
// follow the Go rule in talkoot/roster.go, so a hand-edited value that the
// daemon would refuse never reaches app state.
describe('talkootHref / takeTalkootParams', () => {
  beforeEach(() => at('/talkoot/'))

  it('links bare, to a team, and to a member of a team', () => {
    expect(talkootHref()).toBe('/talkoot/')
    expect(talkootHref('crew')).toBe('/talkoot/?team=crew')
    expect(talkootHref('crew', 'developer')).toBe('/talkoot/?team=crew&member=developer')
  })

  it('drops a member with no team, which names nobody', () => {
    expect(talkootHref('', 'developer')).toBe('/talkoot/')
    at('/talkoot/?member=developer')
    expect(takeTalkootParams()).toEqual({ team: '', member: '' })
    expect(location.search).toBe('')
  })

  it('reads a team and a member and strips both', () => {
    at('/talkoot/?team=crew&member=developer')
    expect(takeTalkootParams()).toEqual({ team: 'crew', member: 'developer' })
    expect(location.search).toBe('')
  })

  it('rejects an id the daemon would refuse, and still strips it', () => {
    for (const bad of ['Crew', '1crew', 'has space', '../x', 'a'.repeat(33)]) {
      at('/talkoot/?team=' + encodeURIComponent(bad))
      expect(takeTalkootParams().team, bad).toBe('')
      expect(location.search).toBe('')
    }
  })

  it('leaves the other apps\' params alone', () => {
    at('/talkoot/?team=crew&session=20260720-182451-27cbde4a')
    expect(takeTalkootParams().team).toBe('crew')
    expect(location.search).toBe('?session=20260720-182451-27cbde4a')
  })
})
