import { describe, expect, it } from 'vitest'
import { readFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { atComplete } from './atcomplete'
import type { WireFileEntry } from '../../platform/ctrlproto/types'

// The SAME golden fixtures Go's widgets.AtComplete test loads
// (packages/agent/modes/widgets/testdata/at_complete_golden.json). Pinning
// both languages to one file keeps the TUI's Tab and the composer's Tab
// behavior-identical — the summarizeToolNames pattern.
const here = dirname(fileURLToPath(import.meta.url))
const fixturePath = join(here, '../../../../../modes/widgets/testdata/at_complete_golden.json')
const cases = JSON.parse(readFileSync(fixturePath, 'utf8')) as {
  name: string
  entries: WireFileEntry[]
  query: string
  want: string
  n: number
}[]

describe('atComplete matches the shared golden fixtures', () => {
  it('loads a meaningful fixture set', () => {
    expect(cases.length).toBeGreaterThanOrEqual(10)
  })
  for (const c of cases) {
    it(c.name, () => {
      const [extended, n] = atComplete(c.entries, c.query)
      expect(extended).toBe(c.want)
      expect(n).toBe(c.n)
    })
  }
})
