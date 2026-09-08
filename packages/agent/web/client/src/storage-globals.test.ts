// @vitest-environment happy-dom
import { beforeEach, describe, expect, it } from 'vitest'

// Guards what vitest.setup.ts repairs.
//
// Node 25 claims `localStorage` as a lazy global and, with no
// --localstorage-file, hands back a plain object with no clear(). It lands on
// globalThis before happy-dom can install its own, so 29 tests across two
// files died on "localStorage.clear is not a function" while CI, on an older
// Node from the alpine packages, stayed green. That split is the reason this
// file exists: without it the repair is invisible, and the next Node that
// changes the rules breaks the suite somewhere unrelated instead of here.
describe('the Web Storage globals', () => {
  beforeEach(() => {
    localStorage.clear()
    sessionStorage.clear()
  })

  for (const [name, storage] of [
    ['localStorage', () => localStorage],
    ['sessionStorage', () => sessionStorage],
  ] as const) {
    describe(name, () => {
      it('is a Storage and not the empty object Node hands out', () => {
        expect(typeof storage().clear).toBe('function')
        expect(typeof storage().getItem).toBe('function')
        expect(typeof storage().setItem).toBe('function')
        expect(typeof storage().removeItem).toBe('function')
      })

      it('round-trips a value, and clear() empties it', () => {
        storage().setItem('scheme', 'dark')
        expect(storage().getItem('scheme')).toBe('dark')
        expect(storage().length).toBe(1)

        storage().clear()
        expect(storage().getItem('scheme')).toBe(null)
        expect(storage().length).toBe(0)
      })

      it('answers null for a key it never held', () => {
        expect(storage().getItem('never-written')).toBe(null)
      })
    })
  }

  // The two are separate stores. A repair that pointed both names at one
  // Storage would pass every check above and still be wrong.
  it('keeps the two stores apart', () => {
    localStorage.setItem('shared-key', 'local')
    sessionStorage.setItem('shared-key', 'session')

    expect(localStorage.getItem('shared-key')).toBe('local')
    expect(sessionStorage.getItem('shared-key')).toBe('session')

    localStorage.clear()
    expect(sessionStorage.getItem('shared-key')).toBe('session')
  })
})
