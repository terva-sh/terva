import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import { describe, expect, it } from 'vitest'
import { REASONING_LEVELS } from './ReasoningPick'

// provider.ReasoningLevels in Go carries this comment:
//
//   "It exists because the three places that spoke about the ladder drifted:
//    the flag accepted "max" while both `--help` and the error a typo produced
//    listed only up to "maximum", so the tier that unlocks gpt-5.6's native
//    ceiling was enforced but never advertised. Printing a hand-written copy of
//    this list is how that happens, so there is no hand-written copy any more."
//
// There is a hand-written copy. REASONING_LEVELS is one, and nothing tied it to
// the Go list — a rung added on one side reaches the other only if somebody
// remembers this file exists, which is the exact failure the Go comment
// describes having already happened once.
//
// This reads the Go source rather than restating it. Restating it here would be
// a fourth copy.
const repoRoot = join(__dirname, '..', '..', '..', '..', '..', '..')

function goReasoningLevels(): string[] {
  const src = readFileSync(join(repoRoot, 'packages/provider/reasoning.go'), 'utf8')
  const m = src.match(/var ReasoningLevels = \[\]string\{([^}]*)\}/)
  if (!m) throw new Error('ReasoningLevels not found in packages/provider/reasoning.go — the anchor moved')
  return [...m[1].matchAll(/"([^"]+)"/g)].map((x) => x[1])
}

describe('reasoning ladder parity', () => {
  it('reads a plausible ladder out of Go', () => {
    // Guards the guard: a regex that silently matched nothing would make every
    // assertion below pass vacuously.
    const go = goReasoningLevels()
    expect(go.length).toBeGreaterThan(4)
    expect(go).toContain('off')
  })

  it('the web ladder is exactly the Go ladder, in order', () => {
    expect(
      [...REASONING_LEVELS],
      'REASONING_LEVELS in ReasoningPick.tsx has drifted from provider.ReasoningLevels. ' +
        'The Go list is the source; a rung offered on one surface and not the other is the ' +
        'bug that list was created to end.',
    ).toEqual(goReasoningLevels())
  })
})
