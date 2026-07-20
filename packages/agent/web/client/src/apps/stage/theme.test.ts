import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { describe, expect, it } from 'vitest'

// Stage ships four themes, and for a long time only its LIBRARY re-skinned: the
// chat screen hardcoded Dusk's palette as rgba()/hex literals, so picking
// Parchment (a light theme) left dark-brown bubbles and a near-black composer.
// .stage-bubble was the tell — it tokenized its border and hardcoded its
// background in the same rule.
//
// The existing Playwright smoke could not see it: it asserts that the
// --stage-bg VARIABLE changed on documentElement, never that any surface
// consumes it, and it never enters a chat. (It also never runs in CI.)
//
// This asserts the invariant directly on the source, which is where the bug
// lives: a value declared as one theme's palette must not appear anywhere else
// in the sheet. Any rule wanting that colour should name the token, so every
// other theme's override reaches it.
const CSS = readFileSync(resolve(__dirname, 'stage.css'), 'utf8')

// The :root blocks that declare the palettes — the base (Dusk) and the
// data-theme presets.
function paletteBlocks(): { name: string; body: string }[] {
  const out: { name: string; body: string }[] = []
  const re = /:root(\[data-theme='([a-z]+)'\])?\s*\{([^}]*)\}/g
  for (const m of CSS.matchAll(re)) out.push({ name: m[2] ?? 'dusk', body: m[3] })
  return out
}

// #rrggbb -> "r, g, b", the shape these literals took inside rgba().
function rgbTriplet(hex: string): string {
  const n = parseInt(hex.slice(1), 16)
  return `${(n >> 16) & 255}, ${(n >> 8) & 255}, ${n & 255}`
}

describe('stage.css theme tokens', () => {
  const blocks = paletteBlocks()

  it('finds the palette blocks it guards', () => {
    expect(blocks.map((b) => b.name).sort()).toEqual(['dusk', 'nocturne', 'parchment', 'rose'])
  })

  it('declares no palette colour outside a :root block', () => {
    // Everything that is not a palette declaration — i.e. the actual rules.
    let rules = CSS
    for (const b of blocks) rules = rules.replace(b.body, '')

    const leaks: string[] = []
    for (const b of blocks) {
      for (const m of b.body.matchAll(/(--stage-[a-z0-9-]+):\s*(#[0-9a-fA-F]{6})\s*;/g)) {
        const [, token, hex] = m
        // Both spellings the leak took: the bare hex, and the rgba() triplet.
        for (const form of [hex.toLowerCase(), rgbTriplet(hex.toLowerCase())]) {
          if (rules.toLowerCase().includes(form)) {
            leaks.push(`${form} (${b.name}'s ${token}) — name the token instead`)
          }
        }
      }
    }
    expect([...new Set(leaks)], leaks.join('\n')).toEqual([])
  })
})
