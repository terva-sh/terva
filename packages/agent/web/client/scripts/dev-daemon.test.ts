import { describe, expect, it } from 'vitest'
import { DAEMON_ROUTES, daemonOrigin, daemonProxy } from './dev-daemon'

const daemon = 'http://127.0.0.1:8730'

describe('daemonOrigin', () => {
  it("rewrites the dev server's own origin to the daemon's", () => {
    expect(daemonOrigin({ origin: 'http://localhost:5173', host: 'localhost:5173' }, daemon)).toBe('http://127.0.0.1:8730')
  })

  it('leaves another origin alone, so the daemon still refuses it', () => {
    expect(daemonOrigin({ origin: 'https://attacker.example', host: 'localhost:5173' }, daemon)).toBeUndefined()
  })

  it('leaves a request with no origin alone', () => {
    expect(daemonOrigin({ host: 'localhost:5173' }, daemon)).toBeUndefined()
  })

  it('leaves an origin that does not parse alone', () => {
    expect(daemonOrigin({ origin: 'null', host: 'localhost:5173' }, daemon)).toBeUndefined()
  })
})

describe('daemonProxy', () => {
  it('proxies every daemon route, and upgrades only /ws', () => {
    const table = daemonProxy(daemon)
    expect(Object.keys(table)).toEqual(DAEMON_ROUTES)
    for (const route of DAEMON_ROUTES) {
      expect(table[route].target).toBe('http://127.0.0.1:8730')
      expect(table[route].ws).toBe(route === '/ws')
    }
  })

  it('refuses an address that is not http', () => {
    expect(() => daemonProxy('127.0.0.1:8730')).toThrow(/http/)
    expect(() => daemonProxy('not a url')).toThrow(/not a URL/)
  })
})
