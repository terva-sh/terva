// The World-delete confirm, and the reason it is one function rather than two
// copies of a sentence.
//
// A World is the case where the reassurance IS the decision: deleting one does
// not take the chats played in it, because a session copies its World in at
// creation and keeps that copy. Someone who thinks otherwise will not delete a
// World they are done with; someone told the wrong thing on one surface and the
// right thing on the other has no idea which to believe.
import { describe, expect, it } from 'vitest'
import { cardDeleteWarning, worldDeleteWarning } from './Library'

describe('worldDeleteWarning', () => {
  it('names the World', () => {
    expect(worldDeleteWarning('Bellhaven')).toContain('Bellhaven')
  })

  it('says what SURVIVES, because that is what the decision turns on', () => {
    expect(worldDeleteWarning('Bellhaven')).toMatch(/keep their own copies/)
  })

  // The contrast with a card is the point, and it is a real difference in the
  // daemon rather than a difference in tone: cards.delete is REFUSED while
  // chats are bound to the card, worlds.delete is not, because a chat survives
  // its World and does not survive its card.
  it('does not borrow the card warning, which promises the opposite', () => {
    expect(cardDeleteWarning('Kobeni')).toMatch(/for good/)
    expect(worldDeleteWarning('Bellhaven')).not.toMatch(/for good/)
  })
})
