// Holds the line on the iOS touch-zoom floor (ui.css). iOS Safari zooms the page
// when a field with a computed font-size below 16px takes focus; a floor of
// max(16px, 1em) on every input/select/textarea, gated to pointer:coarse, is the
// only thing that prevents it. The rule lives in ui.css because both apps import
// that sheet, so this is the one place the floor has to survive.
//
// A unit test rather than a smoke because the Playwright smokes do not run in CI
// — the exact reason Stage shipped without the floor in the first place. This
// reads the source, so it catches deletion, weakening (below 16px), dropping the
// !important, or narrowing away from element selectors — none of which a
// rendered-DOM test on one field would notice.
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { describe, expect, it } from 'vitest'

const css = readFileSync(resolve(__dirname, 'ui.css'), 'utf8')

// The body of the (first) @media (pointer: coarse) { ... } block, braces balanced.
function coarseBlock(sheet: string): string {
  const at = sheet.search(/@media\s*\(\s*pointer:\s*coarse\s*\)\s*\{/)
  expect(at, 'ui.css must carry an @media (pointer: coarse) block — the touch floor').toBeGreaterThanOrEqual(0)
  const open = sheet.indexOf('{', at)
  let depth = 0
  for (let i = open; i < sheet.length; i++) {
    if (sheet[i] === '{') depth++
    else if (sheet[i] === '}' && --depth === 0) return sheet.slice(open + 1, i)
  }
  throw new Error('unbalanced @media (pointer: coarse) block in ui.css')
}

describe('iOS touch-zoom floor (ui.css)', () => {
  const block = coarseBlock(css)

  it('floors text-entry controls at 16px, with !important, by element', () => {
    // The floor value: at least 16px, and !important so it beats `font: inherit`.
    expect(block, 'the coarse block must floor font-size at max(16px, …)').toMatch(
      /font-size:\s*max\(\s*16px\s*,[^;]*\)\s*!important/,
    )
  })

  it('targets input, textarea and select by element, not a class list', () => {
    // Element selectors so a new component field is covered automatically. The
    // whole point is that no one has to remember to opt a field in.
    for (const el of ['input', 'textarea', 'select']) {
      expect(block, `the floor must reach every <${el}>`).toContain(el)
    }
    // checkbox/radio are excluded — flooring their font-size would resize the
    // native control box on some engines, and they never trigger the zoom.
    expect(block).toContain("input:not([type='checkbox'])")
  })

  it('is not duplicated in the panel sheet (single source)', () => {
    const styles = readFileSync(resolve(__dirname, '../styles.css'), 'utf8')
    expect(
      /@media\s*\(\s*pointer:\s*coarse\s*\)[^}]*font-size:\s*max\(\s*16px/.test(styles),
      'the floor moved to ui.css — the panel must not carry a second copy that can drift',
    ).toBe(false)
  })
})
