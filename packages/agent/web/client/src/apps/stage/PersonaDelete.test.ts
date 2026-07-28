import { describe, expect, it } from 'vitest'
import { personaDeleteWarning } from './Library'

// The persona counterpart to cardDeleteWarning, and the contrast is the point.
// Deleting a CARD stops its chats reopening — the card is the character. A
// persona is a voice: the chats still open, because the build falls back to the
// workspace default and says so on load. So this confirm names what changes, not
// what breaks, and the sentences must not be borrowed from the card one.
describe('personaDeleteWarning', () => {
  it('says the delete is final when nothing depends on the persona', () => {
    const msg = personaDeleteWarning('Kartoittaja', 0, false)
    expect(msg).toContain('“Kartoittaja”')
    expect(msg).toMatch(/can't be undone/)
  })

  it('claims no collateral when no chat was created with it', () => {
    expect(personaDeleteWarning('Kartoittaja', 0, false)).not.toMatch(/still opens|still open/)
  })

  it('names the chats and promises they still open', () => {
    const msg = personaDeleteWarning('Kartoittaja', 3, false)
    expect(msg).toContain('3 chats were created with it')
    expect(msg).toMatch(/They still open/)
    // The reassurance is the whole difference from the card case. A user who
    // reads this as "3 chats will break" would keep a persona they meant to
    // remove — and one who is not told at all is surprised by the voice change.
    expect(msg).toMatch(/default persona/)
  })

  it('does not borrow the card warning, which would be a lie here', () => {
    const msg = personaDeleteWarning('Kartoittaja', 3, false)
    expect(msg).not.toMatch(/no longer open/)
    expect(msg).not.toMatch(/for good/)
  })

  it('agrees with itself about a single chat', () => {
    expect(personaDeleteWarning('Kartoittaja', 1, false)).toContain('1 chat was created with it')
  })

  // Deleting a copy that SHADOWS a built-in un-shadows it, so the name those
  // chats replay still resolves — to the built-in it was copied from. There is
  // no fallback and nothing to count, and saying "they'll use your default
  // persona" would be wrong.
  it('says nothing about chats when the delete only un-shadows a built-in', () => {
    const msg = personaDeleteWarning('Kertoja', 7, true)
    expect(msg).toMatch(/built-in of the same name comes back/)
    expect(msg).not.toMatch(/7 chats/)
    expect(msg).not.toMatch(/default persona/)
  })

  // A negative can only come from a count bug, and must not reach the user as
  // "-1 chats" at the moment they approve a delete.
  it('treats an impossible count as none', () => {
    expect(personaDeleteWarning('Kartoittaja', -2, false)).toBe(personaDeleteWarning('Kartoittaja', 0, false))
  })
})
