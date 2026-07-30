import { describe, it, expect } from 'vitest'
import { cardQueryTerms, matchesCardQuery, type CardSearchable } from './cardsearch'

const card = (over: Partial<CardSearchable> = {}): CardSearchable => ({ name: 'Kobeni', ...over })

describe('cardQueryTerms', () => {
  it('lowercases and drops the whitespace a half-typed query carries', () => {
    expect(cardQueryTerms('  Half   Elf ')).toEqual(['half', 'elf'])
  })

  it('is empty for an empty or whitespace-only query', () => {
    expect(cardQueryTerms('')).toEqual([])
    expect(cardQueryTerms('   ')).toEqual([])
  })
})

describe('matchesCardQuery', () => {
  it('matches everything when there are no terms, so a caller need not branch', () => {
    expect(matchesCardQuery(card(), [])).toBe(true)
  })

  it('matches inside a name, not only at a word start', () => {
    expect(matchesCardQuery(card({ name: 'Half-Elf Ranger' }), ['elf'])).toBe(true)
  })

  it('is case-insensitive in both directions', () => {
    expect(matchesCardQuery(card({ name: 'KOBENI' }), cardQueryTerms('kobeni'))).toBe(true)
    expect(matchesCardQuery(card({ name: 'kobeni' }), cardQueryTerms('KOBENI'))).toBe(true)
  })

  it('searches the creator', () => {
    expect(matchesCardQuery(card({ creator: 'Tatsuki' }), ['tatsuki'])).toBe(true)
  })

  // The migration lever: an imported library arrives tagged, because that is
  // how it was organized where it came from.
  it('searches tags, which is how an import can be pulled apart by cluster', () => {
    expect(matchesCardQuery(card({ tags: ['fantasy', 'ranger'] }), ['ranger'])).toBe(true)
  })

  it('ANDs its terms, so each word narrows', () => {
    const c = card({ name: 'Elf Ranger', tags: ['fantasy'] })
    expect(matchesCardQuery(c, cardQueryTerms('elf ranger'))).toBe(true)
    expect(matchesCardQuery(c, cardQueryTerms('elf fantasy'))).toBe(true) // across fields
    expect(matchesCardQuery(c, cardQueryTerms('elf dwarf'))).toBe(false)
  })

  // Fields are joined with a separator no term can contain (terms are split on
  // whitespace), so a multi-word query cannot be satisfied by two unrelated
  // facts abutting each other.
  it('does not match a phrase spanning the boundary between two fields', () => {
    const c = card({ name: 'Ada', creator: 'Lovelace Studios' })
    expect(matchesCardQuery(c, ['ada'])).toBe(true)
    expect(matchesCardQuery(c, ['lovelace'])).toBe(true)
    expect(matchesCardQuery(c, ['ada lovelace'])).toBe(false)
  })

  it('tolerates a card with no creator and no tags', () => {
    expect(matchesCardQuery({ name: 'Kobeni' }, ['kobeni'])).toBe(true)
    expect(matchesCardQuery({ name: 'Kobeni' }, ['fantasy'])).toBe(false)
  })
})
