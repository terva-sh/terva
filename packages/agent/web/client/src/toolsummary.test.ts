import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { dirname, join } from 'node:path'
import { describe, expect, it } from 'vitest'
import { summarizeToolNames } from './toolsummary'

// The SAME golden fixtures the Go summarizer test loads
// (packages/tui/testdata/tool_summary_golden.json). Pinning both languages to
// one file means a change to the summary format on either side fails the
// other's test — the TUI and web summaries cannot silently drift.
const here = dirname(fileURLToPath(import.meta.url))
const fixturePath = join(here, '../../../../tui/testdata/tool_summary_golden.json')
const cases = JSON.parse(readFileSync(fixturePath, 'utf8')) as { names: string[]; want: string }[]

describe('summarizeToolNames matches the shared golden fixtures', () => {
  it('loads fixtures', () => {
    expect(cases.length).toBeGreaterThan(0)
  })
  for (const c of cases) {
    it(JSON.stringify(c.names), () => {
      expect(summarizeToolNames(c.names)).toBe(c.want)
    })
  }
})
