// Guards the shared base in ui.css against the drift that produced three bugs in
// a row (the copy button, the iOS 16px floor, the notch inset): a cross-cutting
// rule that lived in one app's sheet, so the other never got it.
//
// Two invariants, both source-read (so they run in CI, unlike the Playwright
// smokes):
//   1. The --ui-* token contract — every token the base consumes is defined by
//      BOTH apps, and both keep the full documented vocabulary. So a shared rule
//      can never again reference a colour/shape one app hasn't mapped.
//   2. Base-owned resets are not re-declared in an app sheet — the universal
//      box-sizing reset and the form-control shrink floor live only in ui.css,
//      so they can't fork.
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { describe, expect, it } from 'vitest'

const read = (p: string) => readFileSync(resolve(__dirname, p), 'utf8')
const base = read('ui.css')
const APPS: Array<[string, string]> = [
  ['panel (styles.css)', read('../styles.css')],
  ['stage (stage.css)', read('../apps/stage/stage.css')],
]

// The documented --ui-* vocabulary (see ui.css "token contract"). The base and
// the shared ui/ primitives to come may consume any of these, so both apps must
// always define all of them — not just the ones consumed today.
const CONTRACT = [
  '--ui-bg',
  '--ui-surface',
  '--ui-fg',
  '--ui-muted',
  '--ui-line',
  '--ui-accent',
  '--ui-danger',
  '--ui-ok',
  '--ui-radius',
]

// --ui-* names the base actually reads via var(--ui-…).
function consumedUiTokens(sheet: string): string[] {
  const s = new Set<string>()
  for (const m of sheet.matchAll(/var\(\s*(--ui-[a-z0-9-]+)/g)) s.add(m[1])
  return [...s]
}

// Every custom property a sheet declares (`--x:`), anywhere — :root, a
// media/theme override, all count as "this sheet defines --x".
function declaredProps(sheet: string): Set<string> {
  const s = new Set<string>()
  for (const m of sheet.matchAll(/(--[a-z0-9-]+)\s*:/g)) s.add(m[1])
  return s
}

describe('shared --ui-* token contract', () => {
  const consumed = consumedUiTokens(base)

  it('base only consumes tokens that are in the documented contract', () => {
    for (const tok of consumed) {
      expect(
        CONTRACT,
        `ui.css consumes ${tok} but it is not in the documented contract — add it (and map it in both apps)`,
      ).toContain(tok)
    }
  })

  for (const [name, sheet] of APPS) {
    const declared = declaredProps(sheet)

    it(`${name} defines every --ui-* the base consumes`, () => {
      for (const tok of consumed) {
        expect(declared.has(tok), `${name} must map ${tok} — the base consumes it`).toBe(true)
      }
    })

    it(`${name} defines the full contract vocabulary`, () => {
      for (const tok of CONTRACT) {
        expect(declared.has(tok), `${name} is missing ${tok} from its :root mapping`).toBe(true)
      }
    })

    it(`${name} maps each --ui-* onto a token it actually defines`, () => {
      // `--ui-accent: var(--stage-accent)` is a lie if --stage-accent is undefined.
      for (const m of sheet.matchAll(/--ui-[a-z0-9-]+\s*:\s*var\(\s*(--[a-z0-9-]+)\s*\)/g)) {
        expect(declared.has(m[1]), `${name}: a --ui-* maps onto ${m[1]}, which the sheet never defines`).toBe(true)
      }
    })
  }
})

describe('base-owned resets are not re-declared in an app sheet', () => {
  // The universal box-sizing reset: a rule whose selector is `*` (optionally with
  // ::before/::after). A component-scoped `.foo { box-sizing }` is fine and not matched.
  const universalBoxSizing = /(?:^|[{};])\s*\*(?:\s*,\s*\*::[\w-]+)*\s*\{[^}]*box-sizing/
  // The form-control shrink floor's signature grouped selector.
  const shrinkFloor = /select\s*,\s*input\s*,\s*textarea/

  for (const [name, sheet] of APPS) {
    it(`${name} has no universal box-sizing reset (ui.css owns it)`, () => {
      expect(universalBoxSizing.test(sheet), `${name} re-declares * { box-sizing } — remove it; ui.css owns it`).toBe(
        false,
      )
    })
    it(`${name} has no form-control shrink reset (ui.css owns it)`, () => {
      expect(shrinkFloor.test(sheet), `${name} re-declares the "select, input, textarea" shrink floor — ui.css owns it`).toBe(
        false,
      )
    })
  }
})
