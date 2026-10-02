// Text contrast for every palette the web client ships: the panel's light and
// dark schemes and each Stage preset.
//
// The palettes are read from assets/brand/tokens.json, the source tokens.css is
// generated from, so a colour edit is measured the moment it lands. Each pair
// is a text colour on the ground it is drawn on, held to the WCAG AA floor of
// 4.5:1.
//
// Some pairs fail today, and fixing them is a design change rather than a
// one-line repair. KNOWN_FAILURES records them, and the list can only shrink.
// A new failure fails this test, and so does an entry that has started to
// pass, so a fix also has to delete its line. The STE gate's baseline works the
// same way.
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { describe, expect, it } from 'vitest'
import type { Tokens } from './tokens/render'

const tokens: Tokens = JSON.parse(
  readFileSync(resolve(__dirname, '../../../../../../assets/brand/tokens.json'), 'utf8'),
)

function hex(where: string, v: unknown): string {
  if (typeof v !== 'string' || !/^#[0-9a-f]{6}$/i.test(v)) throw new Error(`${where} is ${v ?? 'missing'}, not a six-digit hex`)
  return v
}

function luminance(h: string): number {
  const c = [1, 3, 5].map((i) => parseInt(h.slice(i, i + 2), 16) / 255)
  const [r, g, b] = c.map((x) => (x <= 0.04045 ? x / 12.92 : ((x + 0.055) / 1.055) ** 2.4))
  return 0.2126 * r + 0.7152 * g + 0.0722 * b
}

function ratio(a: string, b: string): number {
  const [hi, lo] = [luminance(a), luminance(b)].sort((x, y) => y - x)
  return (hi + 0.05) / (lo + 0.05)
}

type Palette = Record<string, string>

// The panel: one palette per scheme, plus the scheme-independent intent colours.
function panelPalettes(): Array<[string, Palette]> {
  const shared = ['accent', 'danger', 'warning', 'ok', 'user-fg']
  return (['light', 'dark'] as const).map((scheme) => {
    const p: Palette = {}
    for (const [role, v] of Object.entries(tokens.panel.schemes[scheme])) p[role] = hex(`panel ${scheme} ${role}`, v)
    for (const key of shared) p[key] = hex(`panel ${key}`, tokens.panel.shared[key])
    return [`panel ${scheme}`, p]
  })
}

// Stage: Dusk declares every role, and a preset overrides only what it changes.
function stagePalettes(): Array<[string, Palette]> {
  const { presets } = tokens.stage
  return Object.keys(presets).map((name) => {
    const p: Palette = {}
    for (const role of Object.keys(presets.dusk).filter((k) => k !== '$comment')) {
      p[role] = hex(`stage ${name} ${role}`, presets[name][role] ?? presets.dusk[role])
    }
    return [`stage ${name}`, p]
  })
}

// [text, ground] pairs. Each is a role the app draws as text on that ground.
const PANEL_PAIRS: Array<[string, string]> = [
  ['fg', 'bg'], ['muted', 'bg'], ['muted', 'panel'], ['accent', 'bg'],
  ['danger', 'bg'], ['warning', 'bg'], ['ok', 'bg'], ['user-fg', 'user'],
]
const STAGE_PAIRS: Array<[string, string]> = [
  ['fg', 'bg'], ['fg', 'surface-2'], ['muted', 'surface'], ['muted', 'surface-2'],
  ['accent', 'surface'], ['on-accent', 'accent'], ['ok', 'surface'], ['warn', 'surface'], ['danger', 'surface'],
]

const FLOOR = 4.5

// Below the floor on 2026-10-01, with the ratio measured then. Delete a line
// when its pair passes.
const KNOWN_FAILURES: Record<string, number> = {
  'panel light: accent on bg': 4.34,
  'panel light: warning on bg': 2.72,
  'panel light: ok on bg': 3.45,
  'panel dark: danger on bg': 4.36,
  'panel dark: user-fg on user': 3.68,
  // Parchment overrides the grounds but inherits Dusk's intent colours, which
  // are pale enough for a dark ground and nearly vanish on a light one.
  'stage parchment: muted on surface': 3.47,
  'stage parchment: muted on surface-2': 3.01,
  'stage parchment: accent on surface': 3.81,
  'stage parchment: ok on surface': 1.59,
  'stage parchment: warn on surface': 1.59,
  'stage parchment: danger on surface': 2.25,
}

function measure(): Map<string, number> {
  const out = new Map<string, number>()
  const sets: Array<[Array<[string, Palette]>, Array<[string, string]>]> = [
    [panelPalettes(), PANEL_PAIRS],
    [stagePalettes(), STAGE_PAIRS],
  ]
  for (const [palettes, pairs] of sets) {
    for (const [name, p] of palettes) {
      for (const [text, ground] of pairs) out.set(`${name}: ${text} on ${ground}`, ratio(p[text], p[ground]))
    }
  }
  return out
}

describe('palette text contrast', () => {
  const measured = measure()

  it('meets 4.5:1 everywhere outside the known failures', () => {
    const fresh = [...measured]
      .filter(([pair, r]) => r < FLOOR && !(pair in KNOWN_FAILURES))
      .map(([pair, r]) => `${pair} is ${r.toFixed(2)}:1`)
    expect(fresh, 'new pairs below 4.5:1').toEqual([])
  })

  it('lists no known failure that now passes or no longer exists', () => {
    const stale = Object.keys(KNOWN_FAILURES).filter((pair) => !measured.has(pair) || measured.get(pair)! >= FLOOR)
    expect(stale, 'delete these lines from KNOWN_FAILURES').toEqual([])
  })

  // Teeth: the formula and the parser both have to work for the rest to mean
  // anything. Black on white is 21:1 by definition.
  it('measures known values correctly', () => {
    expect(ratio('#000000', '#ffffff')).toBeCloseTo(21, 5)
    expect(measured.size).toBe(2 * PANEL_PAIRS.length + 4 * STAGE_PAIRS.length)
    // The Stage self-reference left text on the accent unreadable. With the
    // literal restored, Dusk's on-accent pair is well clear of the floor.
    expect(measured.get('stage dusk: on-accent on accent')!).toBeGreaterThan(7)
  })
})
