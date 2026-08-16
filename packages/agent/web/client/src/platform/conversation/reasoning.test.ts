import { describe, expect, it } from 'vitest'
import { reasoningLineText } from './reasoning'

// PARITY TABLE — keep in lockstep with reasoningLineCases in
// packages/agent/modes/reasoning_line_test.go. The TUI and the panel render the
// same wire text, and a user who watches one and then the other must not find
// the provider's markup surviving in only one of them. Same inputs, same
// outputs, same order, so a diff of the two tables is readable.
const cases: Array<[name: string, input: string, want: string]> = [
  ['empty', '', ''],
  ['plain headline', '**Inspecting commit before push**', 'Inspecting commit before push'],
  ['only the current section survives', '**First step**\n\n**Second step**', 'Second step'],
  ['three sections keep the last', '**A**\n\n**B**\n\n**C**', 'C'],
  ['single newlines inside a section collapse', 'Reading the file\nthen the handler', 'Reading the file then the handler'],
  ['prose is squashed to one line', 'Let me analyze:\n\n1. first\n2. second', '1. first 2. second'],
  ['runs of whitespace collapse', '  spaced   out  ', 'spaced out'],
  ['carriage returns do not survive', 'line one\r\nline two', 'line one line two'],
  ['a trailing boundary yields nothing', '**Done thinking**\n\n', ''],
  ['bold inside prose is stripped', 'checking **api.go** now', 'checking api.go now'],
]

describe('reasoningLineText', () => {
  for (const [name, input, want] of cases) {
    it(name, () => {
      expect(reasoningLineText(input)).toBe(want)
    })
  }

  // The row is one row whatever arrives: some providers put multi-paragraph
  // prose in this field, and the panel clips rather than grows because the row
  // sits between the transcript and the composer.
  it('never returns a newline', () => {
    const huge = 'thinking about the problem\n'.repeat(500)
    expect(reasoningLineText(huge)).not.toContain('\n')
  })
})
