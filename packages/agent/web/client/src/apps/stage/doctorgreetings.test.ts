import { describe, expect, it } from 'vitest'
import { greetingIndex } from './CardEditor'
import { fieldLabel } from './carddiff'

// The bracket spelling is a wire contract: the server tells the model to emit
// `alternate_greetings[0]`, the server's allow-list parses it, and the client
// routes on it. If the two parsers disagree the proposal silently no-ops —
// applying it would appear to work and change nothing.
describe('greetingIndex', () => {
  it('reads the index the doctor addressed', () => {
    expect(greetingIndex('alternate_greetings[0]')).toBe(0)
    expect(greetingIndex('alternate_greetings[1]')).toBe(1)
    expect(greetingIndex('alternate_greetings[12]')).toBe(12)
  })

  it('rejects anything that is not an indexed greeting', () => {
    for (const bad of [
      'first_mes',
      'alternate_greetings',
      'alternate_greetings[]',
      'alternate_greetings[x]',
      'alternate_greetings[-1]',
      'alternate_greetings[0',
      'xalternate_greetings[0]',
      'alternate_greetings[0]x',
    ]) {
      expect(greetingIndex(bad), bad).toBeNull()
    }
  })

  it('does not confuse index 0 with "not a greeting"', () => {
    // 0 is falsy; a `if (!gi)` guard would drop every proposal against the
    // FIRST greeting, which is the one a doctor edits most.
    expect(greetingIndex('alternate_greetings[0]')).not.toBeNull()
  })
})

describe('fieldLabel for indexed greetings', () => {
  it('numbers them from one, the way the author counts them on screen', () => {
    expect(fieldLabel('alternate_greetings[0]')).toBe('Alternate greetings #1')
    expect(fieldLabel('alternate_greetings[2]')).toBe('Alternate greetings #3')
  })

  it('leaves the bare field and unknown fields alone', () => {
    expect(fieldLabel('alternate_greetings')).toBe('Alternate greetings')
    expect(fieldLabel('first_mes')).toBe('First message')
    expect(fieldLabel('something_else')).toBe('something_else')
  })
})
