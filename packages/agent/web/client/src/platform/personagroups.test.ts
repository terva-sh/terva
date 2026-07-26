import { describe, expect, it } from 'vitest'
import { personaGroupNames, shelvePersonas } from './personagroups'
import type { PersonaSummary } from './ctrlproto/types'

function p(name: string, group?: string): PersonaSummary {
  return { name, ref: name.toLowerCase(), origin: 'built-in', ...(group === undefined ? {} : { group }) }
}

// The shipped roster's shape, in the order the daemon serves it: the Stage crew
// interleaved with Mieli, then the two team directories.
const ROSTER = [
  p('Dramaturgi', 'Stage'),
  p('Mieli', 'Coding'),
  p('Kertoja', 'Stage'),
  p('YATA-1', 'Deliberation'),
  p('Vartija', 'Review'),
]

describe('shelvePersonas', () => {
  it('orders shelves by first appearance, not alphabetically', () => {
    // Alphabetical would be Coding, Deliberation, Review, Stage — which would
    // quietly overrule the ordering the daemon already chose.
    expect(shelvePersonas(ROSTER).map((s) => s.name)).toEqual(['Stage', 'Coding', 'Deliberation', 'Review'])
  })

  it('keeps each shelf in roster order and loses nobody', () => {
    const shelves = shelvePersonas(ROSTER)
    expect(shelves.find((s) => s.name === 'Stage')!.personas.map((x) => x.name)).toEqual(['Dramaturgi', 'Kertoja'])
    expect(shelves.flatMap((s) => s.personas)).toHaveLength(ROSTER.length)
  })

  it('sinks the ungrouped bucket to the bottom wherever it first appeared', () => {
    const roster = [p('Scratch'), p('Dramaturgi', 'Stage'), p('Mieli', 'Coding')]
    const shelves = shelvePersonas(roster)
    expect(shelves.map((s) => s.name)).toEqual(['Stage', 'Coding', ''])
    expect(shelves.at(-1)!.personas.map((x) => x.name)).toEqual(['Scratch'])
  })

  it('renders flat when a daemon serves no groups at all', () => {
    // The compatibility case: an older daemon sends no `group`, and the roster
    // must look exactly as it always did — not one "Other" heading over all 16.
    const roster = [p('Mieli'), p('Vartija')]
    expect(shelvePersonas(roster)).toEqual([{ name: '', personas: roster }])
  })

  it('renders flat when every persona shares one group', () => {
    const roster = [p('Dramaturgi', 'Stage'), p('Kertoja', 'Stage')]
    expect(shelvePersonas(roster)).toEqual([{ name: '', personas: roster }])
  })

  it('treats a blank group as ungrouped', () => {
    const shelves = shelvePersonas([p('Scratch', '  '), p('Mieli', 'Coding')])
    expect(shelves.map((s) => s.name)).toEqual(['Coding', ''])
  })

  it('has no shelves for an empty roster', () => {
    expect(shelvePersonas([])).toEqual([])
  })
})

describe('personaGroupNames', () => {
  it('lists the existing shelves for an editor to suggest', () => {
    expect(personaGroupNames(ROSTER)).toEqual(['Coding', 'Deliberation', 'Review', 'Stage'])
  })

  it('never offers "ungrouped" as a name to pick', () => {
    expect(personaGroupNames([p('Scratch'), p('Blank', '  '), p('Mieli', 'Coding')])).toEqual(['Coding'])
  })

  it('offers each name once however many personas carry it', () => {
    expect(personaGroupNames([p('a', 'Stage'), p('b', 'Stage'), p('c', 'Stage')])).toEqual(['Stage'])
  })
})
