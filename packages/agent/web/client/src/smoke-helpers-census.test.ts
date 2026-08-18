import { readdirSync, readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { dirname, join } from 'node:path'
import { describe, expect, it } from 'vitest'

// The Stage boot responder and the /media/** placeholder were copy-pasted across
// the smoke suite: 54 files hand-rolled the cards.list/personas.list block and 30
// re-declared the same inline SVG route, while support.ts — the documented home
// for shared setup — offered neither.
//
// Nothing had gone WRONG because of it: the arms each file carried tracked what
// that file needed, and no test booted a daemon missing a verb its neighbours
// served. The cost was the next change to the boot contract, which would have
// had to be made 54 times.
//
// So the rule is stated where a rule can be enforced. A copy that reappears has
// to say why here, rather than being noticed by whoever happens to read two of
// these files in one sitting.
const here = dirname(fileURLToPath(import.meta.url))
const smokeDir = join(here, '../tests/smoke')
const files = readdirSync(smokeDir)
  .filter((f) => f.endsWith('.smoke.ts'))
  .map((f) => ({ name: f, src: readFileSync(join(smokeDir, f), 'utf8') }))

// The placeholder is a fixed 80x80 square. A route that answers /media/** with
// something ELSE — a wide gradient for a background, a real PNG — is not this
// duplication and is not asked to call the helper.
const PLACEHOLDER = /width="80" height="80"><rect width="80" height="80" fill="#[0-9a-fA-F]{6}"\/><\/svg>/

// Bounded, reasoned, and checked for staleness below.
const mediaExemptions: Record<string, string> = {
  'stage.smoke.ts':
    'serves a 400x300 gradient for /backgrounds/ and the square for everything else — the ' +
    'response DIFFERS by URL, which is not what stubMedia is for',
}

describe('the smoke suite does not re-hand-roll its shared setup', () => {
  it('is reading a real suite', () => {
    expect(files.length, 'no smoke files found — this census is scanning nothing').toBeGreaterThan(50)
  })

  it('routes /media/** through stubMedia', () => {
    const offenders = files
      .filter((f) => PLACEHOLDER.test(f.src) && !mediaExemptions[f.name])
      .map((f) => f.name)
    expect(
      offenders,
      `these files re-declare the standard /media/** placeholder instead of calling ` +
        `stubMedia(page):\n  ${offenders.join('\n  ')}`,
    ).toEqual([])
  })

  it('has no stale media exemption', () => {
    for (const [name, why] of Object.entries(mediaExemptions)) {
      const f = files.find((x) => x.name === name)
      expect(f, `mediaExemptions names ${name}, which no longer exists`).toBeTruthy()
      expect(
        f && /page\.route\('\*\*\/media\/\*\*'/.test(f.src),
        `mediaExemptions excuses ${name} (${why}), but it no longer routes /media/** at all — ` +
          `drop the entry, or the licence outlives the reason for it`,
      ).toBe(true)
    }
  })

  // installStageBackend answers these two for every call it wraps, so a file on
  // the helper that also states them is re-stating the floor — the copy this
  // change removed, growing back one file at a time.
  it('does not re-state the floor installStageBackend already serves', () => {
    const problems: string[] = []
    for (const f of files) {
      if (!f.src.includes('installStageBackend(')) continue
      if (/if \(method === 'personas\.list'\) return \{ personas: \[\] \}/.test(f.src)) {
        problems.push(`${f.name}: serves personas.list: [] — installStageBackend's default`)
      }
      if (/if \(method === 'cards\.list'\) return \{ cards: \[\{ id: 'card-1', name: 'Ivy', greetings: 1 \}\] \}/.test(f.src)) {
        problems.push(`${f.name}: serves the default card shelf — omit it, or pass \`cards\``)
      }
    }
    expect(problems, problems.join('\n')).toEqual([])
  })

  // NOT stated here: "a file on the helper must have served cards.list before
  // it was switched". That bound is real — a call that never served the verb
  // would start seeing a card where it used to see {} — but it is a property of
  // the MIGRATION, not of the tree it produced. A file whose arm was deleted
  // because it matched the default is, afterwards, indistinguishable from one
  // that never had an arm. The first version of this census asserted it anyway
  // and flagged eleven files that were correctly migrated.
  //
  // The bound was applied where it could be: the migration switched only files
  // where every installMockBackend call already served cards.list, and left the
  // other twenty on installMockBackend.
})
