import { describe, expect, it } from 'vitest'
import { queueSources, sourceLabel, withoutEntry } from './source'

describe('sourceLabel', () => {
  it('names the sources that terva uses and shows any other as it is', () => {
    expect(sourceLabel('swarm:recap')).toBe('sub-agent report')
    expect(sourceLabel('talkoot:delivery')).toBe('team message')
    expect(sourceLabel('routine:nightly')).toBe('routine:nightly')
  })
})

describe('queueSources', () => {
  it('gives one value for each text', () => {
    expect(queueSources(['a', 'b'], ['', 'swarm:recap'])).toEqual(['', 'swarm:recap'])
    // No list: every text is a person's.
    expect(queueSources(['a', 'b'], undefined)).toEqual(['', ''])
    expect(queueSources(undefined, undefined)).toEqual([])
  })

  it('reads a list of another length as no list', () => {
    expect(queueSources(['a', 'b'], ['swarm:recap'])).toEqual(['', ''])
    expect(queueSources([], ['swarm:recap'])).toEqual([])
  })
})

describe('withoutEntry', () => {
  it('removes a text and its source together', () => {
    const q = { texts: ['X', 'X', 'last'], sources: ['', 'swarm:recap', ''] }
    expect(withoutEntry(q, 0)).toEqual({ texts: ['X', 'last'], sources: ['swarm:recap', ''] })
    expect(withoutEntry(q, 1)).toEqual({ texts: ['X', 'last'], sources: ['', ''] })
    expect(withoutEntry(q, 9)).toEqual(q)
  })
})
