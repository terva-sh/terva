import { describe, expect, it } from 'vitest'
import { cardDeleteWarning, cardInUseMessage } from './Library'

// cards.delete used to be an os.RemoveAll with no in-use check, and a session
// re-resolves SessionMeta.Card on every materialize — so every chat bound to the
// card stopped reopening, permanently, with the bytes gone. The confirm counted
// them and let it happen anyway. The daemon now REFUSES while chats are bound,
// so these two messages split: one asks about an irreversible delete that will
// go through, the other explains one that will not.
describe('cardDeleteWarning', () => {
  it('says the card and avatar are gone for good', () => {
    const msg = cardDeleteWarning('Kobeni')
    expect(msg).toContain('“Kobeni”')
    expect(msg).toMatch(/for good/)
  })

  // It is only ever asked when nothing is bound, so it must not carry the old
  // "chats will break" clause — that outcome is now impossible.
  it('does not claim any chat is about to break', () => {
    expect(cardDeleteWarning('Kobeni')).not.toMatch(/no longer open|still has/)
  })
})

describe('cardInUseMessage', () => {
  it('names the card and how many chats hold it', () => {
    const msg = cardInUseMessage('Kobeni', 3)
    expect(msg).toContain('“Kobeni”')
    expect(msg).toContain('3 chats')
  })

  it('agrees with itself about a single chat', () => {
    expect(cardInUseMessage('Kobeni', 1)).toContain('1 chat.')
  })

  // The refusal is only useful if it says how to get past it, and archiving is
  // the path that keeps the chats — it genuinely releases the card, because an
  // archived transcript is not scanned.
  it('says what to do about it, including archiving', () => {
    expect(cardInUseMessage('Kobeni', 2)).toMatch(/deleted or archived/)
  })

  // It must not read as a question. A user shown "are you sure?" for something
  // the daemon will refuse learns that yes means no.
  it('states a fact rather than asking permission', () => {
    expect(cardInUseMessage('Kobeni', 2)).not.toMatch(/\?/)
  })

  // A zero or negative can only come from a count bug. The message is still
  // shown (the daemon refused, so something IS bound) and must not say "0 chats".
  it('never claims zero chats when it is explaining a refusal', () => {
    for (const n of [0, -2]) {
      expect(cardInUseMessage('Kobeni', n)).not.toMatch(/0 chats?\b/)
      expect(cardInUseMessage('Kobeni', n)).toContain('1 chat.')
    }
  })
})
