// Shelving the persona roster.
//
// A roster of sixteen equal-looking tiles is a wall, not a library. Each persona
// carries a `group` — declared in its frontmatter, or inherited from the team
// directory / extension that shipped it — and this partitions the roster by it.
//
// Pure, and shared by both surfaces that render a roster (the panel landing and
// Stage's library) so the shelving rule cannot drift between them. That drift is
// a shape this codebase has been bitten by before: cross-app machinery whose
// rules lived in one app.

import type { PersonaSummary } from './ctrlproto/types'

// PersonaShelf is one rendered section. `name` is "" for the ungrouped bucket —
// the personas that declared no group and inherited none.
export interface PersonaShelf {
  name: string
  personas: PersonaSummary[]
}

// shelvePersonas partitions a roster into the sections a library renders.
//
// Shelves follow FIRST APPEARANCE in the roster rather than an alphabet: the
// daemon already orders the roster (a user's own personas outrank the built-in
// crew), and re-sorting here would quietly overrule it. The ungrouped bucket is
// the one exception — it sinks to the bottom, because "everything else" is not a
// heading anyone scans for.
//
// A roster that yields a single shelf comes back as one UNNAMED shelf, so the
// caller renders it flat. That covers both the trivial case and the important
// one: an older daemon that serves no `group` at all still gets the plain list
// it has always had, rather than one absurd "Other" heading over the whole
// roster.
export function shelvePersonas(personas: PersonaSummary[]): PersonaShelf[] {
  if (personas.length === 0) return []

  const byName = new Map<string, PersonaSummary[]>()
  for (const p of personas) {
    const name = (p.group ?? '').trim()
    const shelf = byName.get(name)
    if (shelf) shelf.push(p)
    else byName.set(name, [p])
  }

  // Map iterates in insertion order, which IS first appearance.
  const shelves = [...byName].map(([name, ps]) => ({ name, personas: ps }))
  if (shelves.length <= 1) return [{ name: '', personas }]

  // A stable sort on one boolean key: the ungrouped bucket moves to the end and
  // every named shelf keeps the order it was found in.
  return shelves.sort((a, b) => Number(a.name === '') - Number(b.name === ''))
}

// personaGroupNames lists the shelves that already exist, for an editor to offer
// as suggestions. Ungrouped is not a name, so it never appears.
export function personaGroupNames(personas: PersonaSummary[]): string[] {
  const seen = new Set<string>()
  for (const p of personas) {
    const name = (p.group ?? '').trim()
    if (name) seen.add(name)
  }
  return [...seen].sort((a, b) => a.localeCompare(b))
}
