// @vitest-environment happy-dom
//
// The portraits preference. Tiny surface, but every assertion here is about a
// default or a failure mode rather than the happy path — the happy path is one
// attribute.
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { applyPortraits, portraitsOn } from './portraits'

beforeEach(() => {
  const store = new Map<string, string>()
  vi.stubGlobal('localStorage', {
    getItem: (k: string) => store.get(k) ?? null,
    setItem: (k: string, v: string) => void store.set(k, String(v)),
    removeItem: (k: string) => void store.delete(k),
    clear: () => store.clear(),
    key: (i: number) => [...store.keys()][i] ?? null,
    get length() {
      return store.size
    },
  })
  document.documentElement.removeAttribute('data-portraits')
})
afterEach(() => vi.unstubAllGlobals())

describe('the portraits preference', () => {
  // Portraits ON unless turned off. A library whose art silently vanished after
  // an upgrade reads as broken, not as configured.
  it('defaults to on', () => {
    expect(portraitsOn()).toBe(true)
  })

  it('persists across a reload and stamps the root', () => {
    applyPortraits(false)
    expect(document.documentElement.getAttribute('data-portraits')).toBe('off')
    expect(portraitsOn()).toBe(false)
  })

  // The default state has NO marker, so a stylesheet cannot key off the wrong
  // value and a future `[data-portraits]` selector cannot match the on state by
  // accident.
  it('removes the attribute rather than setting it to "on"', () => {
    applyPortraits(false)
    applyPortraits(true)
    expect(document.documentElement.hasAttribute('data-portraits')).toBe(false)
    expect(portraitsOn()).toBe(true)
  })

  // A private-mode browser throws on storage. The preference should still hold
  // for this session rather than taking the page down.
  it('survives storage being unavailable', () => {
    vi.stubGlobal('localStorage', {
      getItem: () => {
        throw new Error('denied')
      },
      setItem: () => {
        throw new Error('denied')
      },
    })
    expect(portraitsOn()).toBe(true)
    expect(() => applyPortraits(false)).not.toThrow()
    expect(document.documentElement.getAttribute('data-portraits')).toBe('off')
  })
})
