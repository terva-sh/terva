// Free-text search over the card library.
//
// Pure so the matching rule is unit-tested rather than asserted through a
// rendered grid (vitest runs in CI; see cardsearch.test.ts).
//
// The fields searched are the ones an imported card actually arrives with:
// its name, its creator, and its TAGS. Tags matter more than they look. A
// library that lands here from another front end comes tagged — that is how it
// was organized where it came from — so searching them is what lets a flat
// import be pulled apart along the lines its author already drew, a cluster at
// a time, instead of card by card.
//
// Not searched: description, personality, the greeting, the embedded lorebook.
// They are long free prose, and matching them turns a query meant to name a
// character into one that hits every card mentioning it in passing. The
// library's job here is to FIND a card, not to grep the writing.

// CardSearchable is the shape the matcher needs — a subset of CardSummary, so
// the rule can be tested without building a whole card.
export interface CardSearchable {
  name: string
  creator?: string
  tags?: string[]
}

// cardQueryTerms splits a raw query into lowercased terms, dropping the
// whitespace a half-typed query is full of.
//
// Split ONCE per query rather than per card: this runs over the whole library
// on every keystroke, and the libraries worth searching are the big ones.
export function cardQueryTerms(query: string): string[] {
  return query.toLowerCase().split(/\s+/).filter(Boolean)
}

// matchesCardQuery reports whether a card matches every term (AND, not OR).
//
// AND because the query is a NARROWING tool: each word you add should cut the
// grid down, which is the behaviour a search box is expected to have. Under OR,
// typing more would show more, and a second word could only ever make the
// result harder to use.
//
// Terms match anywhere, not just at a word start — "elf" finds "Half-Elf", and
// a partial name finds the card while you are still typing it.
//
// No terms matches everything, so a caller can apply this unconditionally
// rather than branching on an empty query.
export function matchesCardQuery(c: CardSearchable, terms: string[]): boolean {
  if (terms.length === 0) return true
  // Joined with a separator no term can span, so a query cannot match across
  // the boundary between two fields ("ada lovelace" must not be satisfied by a
  // card named "Ada" whose creator is "Lovelace Studios" — the two words are
  // in different facts about the card).
  const hay = [c.name, c.creator ?? '', ...(c.tags ?? [])].join('\n').toLowerCase()
  return terms.every((term) => hay.includes(term))
}
