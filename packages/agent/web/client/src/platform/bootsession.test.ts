import { describe, expect, it } from 'vitest'
import { pickBootTarget } from './bootsession'

describe('pickBootTarget', () => {
  const ids = ['a', 'b', 'c']

  it('prefers a valid deep link over everything', () => {
    expect(pickBootTarget({ linked: 'b', remembered: 'c', sessionIds: ids })).toBe('b')
  })

  it("falls back to this tab's remembered session when there is no deep link", () => {
    expect(pickBootTarget({ linked: '', remembered: 'c', sessionIds: ids })).toBe('c')
  })

  it('NEVER adopts a global current: no deep link + no memory ⇒ the landing', () => {
    // The whole point of the fix — a fresh tab adopts nothing, so two clients
    // cannot converge on one session.
    expect(pickBootTarget({ linked: '', remembered: '', sessionIds: ids })).toBe('')
  })

  it('drops a stale deep link and falls through to memory', () => {
    expect(pickBootTarget({ linked: 'gone', remembered: 'a', sessionIds: ids })).toBe('a')
  })

  it('drops a stale remembered id and lands on the picker', () => {
    expect(pickBootTarget({ linked: '', remembered: 'deleted', sessionIds: ids })).toBe('')
  })

  it('lands on the picker when the workspace has no sessions at all', () => {
    expect(pickBootTarget({ linked: 'a', remembered: 'b', sessionIds: [] })).toBe('')
  })
})
