import { readdirSync, readFileSync, statSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { dirname, join } from 'node:path'
import { describe, expect, it } from 'vitest'

// Every place that renders ReasoningPick must hand it the model's ladder.
//
// The trap this exists for, found the hard way: when the picker stopped holding
// a hardcoded budget table and started rendering the daemon's rows, a caller
// that passed nothing kept COMPILING — `rungs` is optional, because a model
// that genuinely takes no thinking setting has no ladder. Stage was such a
// caller, and it would have rendered "this model takes no thinking setting" for
// seven rungs of a model that reasons fine. A confident wrong answer, and type-
// checking cannot see it: absent and "this model has none" are the same value.
//
// Written self-enrolling — the call sites are SCANNED, never listed — so a
// third surface that renders the picker enrolls itself on its first run.
const here = dirname(fileURLToPath(import.meta.url))
const srcRoot = join(here, '..')

function tsxFiles(dir: string): string[] {
  const out: string[] = []
  for (const entry of readdirSync(dir)) {
    const full = join(dir, entry)
    if (statSync(full).isDirectory()) {
      out.push(...tsxFiles(full))
    } else if (entry.endsWith('.tsx') && !entry.endsWith('.test.tsx')) {
      out.push(full)
    }
  }
  return out
}

// The opening tag through its closing '>' — enough to see the props, and it
// stops at the first '/>' or '>' so a later sibling's props cannot count.
function openingTags(src: string, component: string): string[] {
  const out: string[] = []
  for (const m of src.matchAll(new RegExp(`<${component}\\b`, 'g'))) {
    const rest = src.slice(m.index!)
    const end = rest.search(/\/>|>/)
    out.push(end < 0 ? rest : rest.slice(0, end))
  }
  return out
}

describe('every ReasoningPick call site passes the ladder', () => {
  const files = tsxFiles(srcRoot).filter(
    (f) => !f.endsWith('ReasoningPick.tsx') && readFileSync(f, 'utf8').includes('<ReasoningPick'),
  )

  it('finds the call sites at all', () => {
    // Teeth for the scan itself: a broken glob would make every assertion below
    // pass over an empty list.
    expect(files.length).toBeGreaterThanOrEqual(2)
  })

  it('hands each one a rungs prop', () => {
    for (const f of files) {
      for (const tag of openingTags(readFileSync(f, 'utf8'), 'ReasoningPick')) {
        expect(tag, `${f}: <ReasoningPick> renders without rungs — every rung would\n` +
          `read "this model takes no thinking setting". Pass the current model's\n` +
          `ladder from models.list (ModelInfo.ladder into reasoning_ladders).`)
          .toContain('rungs=')
      }
    }
  })

  it('hands each one maxIsNative, or the top rung loses its note', () => {
    for (const f of files) {
      for (const tag of openingTags(readFileSync(f, 'utf8'), 'ReasoningPick')) {
        expect(tag, `${f}: <ReasoningPick> renders without maxIsNative — "max" is the\n` +
          `one rung a user may be choosing that does not exist on this model, and\n` +
          `without this it silently reads as an ordinary tier.`)
          .toContain('maxIsNative=')
      }
    }
  })
})

// `global` is a REQUIRED prop, so the type checker guarantees a call site
// passes something — and both call sites passed the empty string, which is
// where this bug lived. The picker's inherit row then read "follow the global
// setting" while declining to say what the global was, on every surface, for as
// long as the component had existed.
//
// The value is not the client's to invent: it is workspace state, and the
// daemon publishes it as the `reasoning` SettingItem. An empty literal here
// means somebody wired the prop rather than the value.
describe('no ReasoningPick call site hardcodes an empty global', () => {
  const files = tsxFiles(srcRoot).filter(
    (f) => !f.endsWith('ReasoningPick.tsx') && readFileSync(f, 'utf8').includes('<ReasoningPick'),
  )

  it('passes a real global everywhere', () => {
    for (const f of files) {
      for (const tag of openingTags(readFileSync(f, 'utf8'), 'ReasoningPick')) {
        expect(
          /global=(""|'')/.test(tag),
          `${f}: <ReasoningPick global=""> — the inherit row will say "follow the ` +
            `global setting" without naming it. Read the daemon's \`reasoning\` ` +
            `SettingItem from the settings surface and pass that.`,
        ).toBe(false)
      }
    }
  })
})
