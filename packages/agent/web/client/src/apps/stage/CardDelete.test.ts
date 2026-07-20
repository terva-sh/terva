import { describe, expect, it } from 'vitest'
import { cardDeleteWarning } from './Library'

// The house style for a destructive confirm in Stage is to say what SURVIVES
// ("its chats keep their own copies — only the saved World goes"). A card is the
// case where that reassurance would be a lie: cards.delete is an os.RemoveAll on
// the card's directory with no in-use check, and a session re-resolves
// SessionMeta.Card on every materialize — so every chat bound to the card stops
// reopening. Nothing warns later and nothing brings it back.
describe('cardDeleteWarning', () => {
  it('says the card and avatar are gone for good', () => {
    const msg = cardDeleteWarning('Kobeni', 0)
    expect(msg).toContain('“Kobeni”')
    expect(msg).toMatch(/for good/)
  })

  it('claims no collateral when nothing is bound to the card', () => {
    expect(cardDeleteWarning('Kobeni', 0)).not.toMatch(/no longer open/)
  })

  it('names the chats that will stop opening, and points at Export', () => {
    const msg = cardDeleteWarning('Kobeni', 3)
    expect(msg).toContain('3 chats with Kobeni will no longer open')
    // Export is the only way to get the card back, and the sheet has that button
    // directly above Delete — the confirm is the moment to say so.
    expect(msg).toMatch(/Export the card first/)
  })

  it('agrees with itself about a single chat', () => {
    expect(cardDeleteWarning('Kobeni', 1)).toContain('1 chat with Kobeni will no longer open')
  })

  // A negative can only come from a count bug, and must not reach the user as
  // "-1 chats" at the moment they approve something irreversible.
  it('treats an impossible count as none', () => {
    expect(cardDeleteWarning('Kobeni', -2)).toBe(cardDeleteWarning('Kobeni', 0))
  })
})
