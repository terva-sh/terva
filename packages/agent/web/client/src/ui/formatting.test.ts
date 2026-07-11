import { describe, expect, it } from 'vitest'
import { compact, humanBytes, humanCount, truncate } from './formatting'

describe('shared formatting', () => {
  it('formats byte and compact number thresholds', () => {
    expect(humanBytes(1023)).toBe('1023 B')
    expect(humanBytes(1536)).toBe('1.5 KB')
    expect(humanBytes(1 << 20)).toBe('1.0 MB')
    expect(humanCount(999)).toBe('999')
    expect(humanCount(1500)).toBe('2k')
    expect(humanCount(1_500_000)).toBe('1.5M')
  })

  it('truncates strings and compact JSON at the existing limit', () => {
    expect(truncate('abcd', 3)).toBe('abc…')
    expect(truncate('abc', 3)).toBe('abc')
    expect(compact({ value: 'x'.repeat(250) })).toHaveLength(201)
  })

  it('returns an empty compact value for cyclic input', () => {
    const cyclic: { self?: unknown } = {}
    cyclic.self = cyclic
    expect(compact(cyclic)).toBe('')
  })
})
