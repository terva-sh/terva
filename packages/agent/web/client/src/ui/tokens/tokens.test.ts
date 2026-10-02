// Holds src/ui/tokens.css to assets/brand/tokens.json. The file snapshot fails
// a plain `vitest run` when the two disagree, and `just design-tokens` (vitest
// with -u, scoped to this file) rewrites it.
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { describe, expect, it } from 'vitest'
import { renderTokensCss, type Tokens } from './render'

const tokens = (): Tokens =>
  JSON.parse(readFileSync(resolve(__dirname, '../../../../../../../assets/brand/tokens.json'), 'utf8'))

describe('design tokens', () => {
  it('tokens.css is the rendering of assets/brand/tokens.json', async () => {
    await expect(renderTokensCss(tokens())).toMatchFileSnapshot('../tokens.css')
  })

  // The renderer's refusals are what keep a malformed table out of the CSS.
  it('refuses a light scheme role with no dark value', () => {
    const t = tokens()
    t.panel.schemes.light.extra = '#123456'
    expect(() => renderTokensCss(t)).toThrow(/panel\.schemes\.light\.extra has no dark value/)
  })

  it('refuses a preset role that Dusk does not declare', () => {
    const t = tokens()
    t.stage.presets.rose.glow = '#123456'
    expect(() => renderTokensCss(t)).toThrow(/rose\.glow has no Dusk value/)
  })

  it('refuses a value that is not a colour', () => {
    const t = tokens()
    t.panel.shared.accent = 'var(--accent)'
    expect(() => renderTokensCss(t)).toThrow(/panel\.shared\.accent is "var\(--accent\)", not a colour/)
  })
})
