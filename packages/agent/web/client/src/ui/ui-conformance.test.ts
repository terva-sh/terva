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
  '--ui-warn',
  '--ui-ok',
  '--ui-radius',
]

// --ui-* names the base actually reads via var(--ui-…).
function consumedUiTokens(sheet: string): string[] {
  const s = new Set<string>()
  for (const m of sheet.matchAll(/var\(\s*(--ui-[a-z0-9-]+)/g)) s.add(m[1])
  return [...s]
}

// Not every --ui-* the base reads is a PALETTE token an app must map. Some are
// the base's own derived values (--ui-deadline-tint, mixed from two palette
// tokens) and some are runtime inputs a component sets inline (--ui-urgency).
// Both are declared in ui.css itself, so "the base declares it" is the test for
// "this is not an app's obligation" — and it stays honest, because a token the
// base merely reads still has to be mapped by both apps.
function appOwedTokens(sheet: string): string[] {
  const owned = declaredProps(sheet)
  return consumedUiTokens(sheet).filter((t) => !owned.has(t))
}

// Every custom property a sheet declares (`--x:`), anywhere — :root, a
// media/theme override, all count as "this sheet defines --x".
function declaredProps(sheet: string): Set<string> {
  const s = new Set<string>()
  for (const m of sheet.matchAll(/(--[a-z0-9-]+)\s*:/g)) s.add(m[1])
  return s
}

describe('shared --ui-* token contract', () => {
  const consumed = appOwedTokens(base)

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

describe('safe-area insets go through --safe-*, not raw env()', () => {
  // The bug this guards (twice now): env(safe-area-inset-*) can go stale to 0
  // after a fixed overlay on iOS, dropping a top bar under the notch. ui.css
  // freezes the insets into --safe-* (ui/safearea.ts writes the frozen values);
  // an app sheet must read var(--safe-*), never env() directly, or it re-opens
  // the bug on any surface near an overlay. See ui.css "safe-area insets".
  const rawEnv = /env\(\s*safe-area-inset-/
  const SIDES = ['top', 'right', 'bottom', 'left']

  it('ui.css declares all four --safe-* variables (the one sanctioned env() home)', () => {
    const declared = declaredProps(base)
    for (const side of SIDES) {
      expect(declared.has(`--safe-${side}`), `ui.css must declare --safe-${side}`).toBe(true)
    }
  })

  it('ui.css seeds --safe-* from env() (the correct pre-freeze / no-JS fallback)', () => {
    for (const side of SIDES) {
      expect(
        new RegExp(`--safe-${side}\\s*:\\s*env\\(\\s*safe-area-inset-${side}`).test(base),
        `ui.css must seed --safe-${side} from env(safe-area-inset-${side})`,
      ).toBe(true)
    }
  })

  for (const [name, sheet] of APPS) {
    it(`${name} uses no raw env(safe-area-inset-*) — read var(--safe-*) instead`, () => {
      expect(
        rawEnv.test(sheet),
        `${name} uses env(safe-area-inset-*) directly — replace it with var(--safe-*); ui.css owns the env() seed`,
      ).toBe(false)
    })
  }
})

describe('every button has press feedback (the base :active)', () => {
  // The tap-flash is suppressed globally, so without a base :active rule a tap
  // gives no sign it registered — the panel had none at all. This can't verify
  // the VISUAL, only that the shared base still declares press feedback for every
  // button via a low-specificity button:active with a real cue.
  it('ui.css declares the base button:active scale feedback', () => {
    expect(
      /button:active\s*\{\s*transform:\s*scale\(/.test(base),
      'ui.css must give every button press feedback via a base `button:active { transform: scale(...) }`',
    ).toBe(true)
  })
})

// Deadline urgency is a shared LOOK, and the reason it is shared is that the
// alternative — each surface picking its own amber — is what left a dozen raw
// copies of one warning colour across the two sheets. These pin the parts that
// make it palette-driven rather than bespoke: the ramp is mixed from tokens,
// and the two states are drawn with tokens.
describe('deadline urgency is drawn from the palette', () => {
  it('mixes the ramp from --ui-warn toward --ui-danger by --ui-urgency', () => {
    expect(
      /--ui-deadline-tint:\s*color-mix\([^)]*var\(--ui-danger\)[^;]*var\(--ui-urgency[^;]*var\(--ui-warn\)/.test(base),
      'ui.css must mix the deadline tint from --ui-warn toward --ui-danger by --ui-urgency, not from a literal',
    ).toBe(true)
  })

  it('draws the urgent ring with --ui-danger', () => {
    expect(
      /\.ui-deadline--urgent\s*\{[^}]*outline:[^}]*var\(--ui-danger\)/.test(base),
      'ui.css must draw the .ui-deadline--urgent ring from --ui-danger',
    ).toBe(true)
  })

  // outline, not border: it costs no layout, so the ring cannot shift a row
  // when it appears, and it does not fight a consumer's own border.
  it('rings with outline so the row does not shift when it lands', () => {
    expect(/\.ui-deadline--urgent\s*\{[^}]*border:/.test(base), '.ui-deadline--urgent must not use border').toBe(false)
  })

  for (const [name, sheet] of APPS) {
    it(`${name} does not restate the deadline look`, () => {
      expect(
        /\.ui-deadline[a-z-]*\s*\{/.test(sheet),
        `${name} styles a .ui-deadline* class — ui.css owns that look so both apps share it`,
      ).toBe(false)
    })
  }
})

// Wide content must scroll inside the message it belongs to, not push the
// message wider than the screen. This is a whole class of bug rather than one
// rule: markdown-it can put three things in a bubble that refuse to wrap — a
// fenced code block, a table, and a long unbroken token — and each has its own
// escape. The panel had the code and table rules; Stage rendered the same HTML
// under a class of its own and got neither, so on a phone a stat block simply
// ran out through the side of the bubble.
//
// Source-read, because vitest runs in CI and the Playwright smokes do not — the
// same reason the rest of this file reads sheets rather than a rendered page. It
// pins the DECLARATION, not the pixels; the screenshot is what confirms those.
describe('wide markdown content is contained, not overflowing', () => {
  // A rule's body, given a literal selector. Naive but sufficient: these
  // selectors appear once, and a second definition would show up as a miss.
  const ruleBody = (sheet: string, selector: string): string => {
    const at = sheet.indexOf(selector + ' {')
    if (at < 0) return ''
    return sheet.slice(at, sheet.indexOf('}', at))
  }

  // Fenced blocks carry markdown.ts's .code-wrap on EVERY surface, so the base
  // owning them is what makes them work in both apps at once.
  it('ui.css contains fenced code blocks for every surface', () => {
    const body = ruleBody(base, '.code-wrap pre')
    expect(body, 'ui.css must style `.code-wrap pre` — markdown.ts emits it on every surface').not.toBe('')
    expect(/overflow-x:\s*auto/.test(body), '`.code-wrap pre` needs overflow-x: auto or a long line grows the layout').toBe(true)
    // Without this the block is a scroll container that still reports its full
    // content width to a flex parent, which drags the bubble wide anyway.
    expect(/max-width:\s*100%/.test(body), '`.code-wrap pre` needs max-width: 100% — overflow-x alone does not bound it').toBe(true)
  })

  // Tables have no wrapper to key on, so each app's own markdown-body class
  // owns them. Both must, or one app scrolls and the other overflows.
  const TABLE_OWNERS: Array<[string, string, string]> = [
    ['panel (styles.css)', read('../styles.css'), '.md table'],
    ['stage (stage.css)', read('../apps/stage/stage.css'), '.stage-bubble table'],
  ]
  for (const [name, sheet, selector] of TABLE_OWNERS) {
    it(`${name} contains a wide table inside the message`, () => {
      const body = ruleBody(sheet, selector)
      expect(body, `${name} must style \`${selector}\``).not.toBe('')
      expect(/overflow-x:\s*auto/.test(body), `${selector} needs overflow-x: auto`).toBe(true)
      // A table is not a block box; overflow-x is ignored until it is one.
      expect(/display:\s*block/.test(body), `${selector} needs display: block or overflow-x does nothing on a table`).toBe(true)
      expect(/max-width:\s*100%/.test(body), `${selector} needs max-width: 100%`).toBe(true)
    })
  }
})
