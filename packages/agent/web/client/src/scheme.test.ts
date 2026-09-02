// @vitest-environment happy-dom
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { beforeEach, describe, expect, it } from 'vitest'

import { applyScheme, currentScheme, nextScheme, schemeGlyph } from './scheme'

// The panel had no light/dark control at all: styles.css carried one
// `@media (prefers-color-scheme: dark)` block and nothing could override it, so
// a person whose OS is dark but who wants the panel light had no way to say so.
//
// The preference is one attribute on <html> and the whole switch is in CSS, so
// the part that can silently rot is the SELECTOR, not the TypeScript. The last
// describe below guards it against the source, because that is the failure the
// runtime tests cannot see: broaden the media arm back to `:root` and every
// unit test here still passes while `light` stops working on a dark OS.

describe('the stored preference', () => {
  beforeEach(() => {
    localStorage.clear()
    document.documentElement.removeAttribute('data-scheme')
  })

  it('starts on auto, so an untouched panel follows the OS', () => {
    expect(currentScheme()).toBe('auto')
  })

  it('reads back what was applied', () => {
    applyScheme('dark')
    expect(currentScheme()).toBe('dark')
    expect(document.documentElement.getAttribute('data-scheme')).toBe('dark')
  })

  it('writes the attribute for auto too, rather than removing it', () => {
    // The CSS matches :not([data-scheme='light']):not([data-scheme='dark']), so
    // either shape works — but leaving a stale `dark` attribute behind while
    // storage says `auto` would desync the button from the page.
    applyScheme('dark')
    applyScheme('auto')
    expect(document.documentElement.getAttribute('data-scheme')).toBe('auto')
  })

  it('falls back to auto on a junk or stale stored value', () => {
    localStorage.setItem('terva_scheme', 'parchment')
    expect(currentScheme()).toBe('auto')
  })

  it('ignores a junk argument instead of pinning an unmatched attribute', () => {
    applyScheme('sepia' as never)
    expect(document.documentElement.getAttribute('data-scheme')).toBe('auto')
  })
})

describe('the cookie mirror', () => {
  beforeEach(() => {
    localStorage.clear()
    document.documentElement.removeAttribute('data-scheme')
    document.cookie = 'terva_scheme=; Path=/; Max-Age=0'
  })

  // The login page is server-rendered under `default-src 'none'` with no
  // script-src, so it cannot read localStorage. The cookie is the only channel
  // that reaches it without adding a script to the page that accepts the bearer
  // token. login.go reads exactly this name and these two values.
  it('mirrors an explicit choice where the server can read it', () => {
    applyScheme('dark')
    expect(document.cookie).toContain('terva_scheme=dark')
    applyScheme('light')
    expect(document.cookie).toContain('terva_scheme=light')
    expect(document.cookie).not.toContain('terva_scheme=dark')
  })

  // auto is written out rather than deleted, so the cookie always states the
  // real preference. loginScheme allowlists light and dark, so `auto` arrives
  // and is correctly treated as no override.
  it('writes auto too, rather than leaving a stale override behind', () => {
    applyScheme('dark')
    applyScheme('auto')
    expect(document.cookie).toContain('terva_scheme=auto')
    expect(document.cookie).not.toContain('terva_scheme=dark')
  })

  it('never mirrors a junk value the server would have to reject', () => {
    applyScheme('sepia' as never)
    expect(document.cookie).toContain('terva_scheme=auto')
    expect(document.cookie).not.toContain('sepia')
  })
})

describe('the header button cycle', () => {
  it('runs auto → light → dark → auto', () => {
    expect(nextScheme('auto')).toBe('light')
    expect(nextScheme('light')).toBe('dark')
    expect(nextScheme('dark')).toBe('auto')
  })

  it('reaches every state from any state', () => {
    let s = nextScheme('sepia' as never)
    const seen = new Set([s])
    for (let i = 0; i < 3; i++) seen.add((s = nextScheme(s)))
    expect([...seen].sort()).toEqual(['auto', 'dark', 'light'])
  })

  it('gives each state its own glyph', () => {
    const glyphs = ['auto', 'light', 'dark'].map((s) => schemeGlyph(s as never))
    expect(new Set(glyphs).size, `glyphs must be distinguishable: ${glyphs.join(' ')}`).toBe(3)
  })

  it('pins the sun to text presentation', () => {
    // The birch-tar sun is only possible because U+2600 renders as a text glyph
    // that takes CSS `color`. VS15 says so explicitly, so a font substitution
    // cannot return a colour emoji that ignores .icon.scheme-light. Deleting the
    // escape looks like harmless tidying and would silently un-style the button
    // on some platforms and not others, which is why it is asserted here.
    expect(schemeGlyph('light')).toBe('\u2600\uFE0E')
    expect(schemeGlyph('dark'), 'U+263E is not emoji, so it needs no selector').toBe('\u263E')
  })
})

const CSS = readFileSync(resolve(__dirname, 'styles.css'), 'utf8')

// The selector that guards the media arm. Both exclusions must be present: drop
// the `light` one and an explicit light choice loses to a dark OS; drop the
// `dark` one and the two dark arms merely agree, which is harmless but means the
// exclusion was edited without understanding, so this asserts the pair.
const MEDIA_ARM = /@media \(prefers-color-scheme: dark\) \{\s*([^{]*)\{/

describe('styles.css keys the palette off data-scheme', () => {
  it('narrows the media arm so an explicit choice wins over the OS', () => {
    const arm = CSS.match(MEDIA_ARM)
    expect(arm, 'the prefers-color-scheme block moved or was renamed').toBeTruthy()
    const selector = arm![1].trim()
    expect(selector, 'an explicit light choice must survive a dark OS').toContain(
      ":not([data-scheme='light'])",
    )
    expect(selector, 'chosen dark is applied by its own rule, not this one').toContain(
      ":not([data-scheme='dark'])",
    )
  })

  it('still applies to a document with no attribute at all', () => {
    // A cached index.html whose script has not run yet has no data-scheme. It
    // must follow the OS, not fall back to light — so the arm must be an
    // exclusion (:not) and never a positive match on [data-scheme='auto'].
    const selector = CSS.match(MEDIA_ARM)![1].trim()
    expect(selector).not.toContain("[data-scheme='auto']")
  })

  it('gives chosen dark its own rule', () => {
    expect(CSS).toMatch(/:root\[data-scheme='dark'\]\s*\{/)
  })

  it('pins color-scheme on both explicit arms, not just the variables', () => {
    // var() cannot reach form controls, scrollbars or the canvas: without this
    // a dark panel on a light OS keeps white select popups.
    const dark = CSS.match(/:root\[data-scheme='dark'\]\s*\{([^}]*)\}/)
    const light = CSS.match(/:root\[data-scheme='light'\]\s*\{([^}]*)\}/)
    expect(dark![1]).toMatch(/color-scheme:\s*dark/)
    expect(light![1]).toMatch(/color-scheme:\s*light/)
  })

  it('declares each palette literal exactly once', () => {
    // The reason the switch re-maps tokens instead of restating colours: two
    // arms now want the dark palette, and a copied hex would drift between them.
    for (const token of ['bg', 'fg', 'muted', 'line', 'panel', 'user']) {
      for (const arm of ['light', 'dark']) {
        const decls = CSS.match(new RegExp(`--c-${token}-${arm}:`, 'g')) ?? []
        expect(decls.length, `--c-${token}-${arm} must be declared once`).toBe(1)
      }
    }
  })
})
