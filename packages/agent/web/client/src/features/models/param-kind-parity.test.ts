import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import { describe, expect, it } from 'vitest'

// The daemon describes each model setting and this client renders whatever it
// was handed, which works right up until the daemon learns a KIND the client
// does not know. Then the row still arrives, still renders, and renders wrong:
// an unknown kind falls through to the text input, so a tri-state becomes a box
// you type "on" into and a list becomes one where you guess the separator.
//
// That is not hypothetical. The capability tri-states existed in the registry
// and in the TUI for months while this form showed nothing at all, because
// nothing connected "the daemon can send this" to "the client renders it". An
// operator's only route to the setting was hand-editing models.json, on every
// machine they used.
//
// So this reads the Go switch that decides what can arrive, rather than
// restating its output here. A list restated here is a list that goes stale.
// Same mechanism as verbs.test.ts and reasoning-ladder-parity.test.ts.
const repoRoot = join(__dirname, '..', '..', '..', '..', '..', '..', '..')

function read(rel: string): string {
  return readFileSync(join(repoRoot, rel), 'utf8')
}

// Every wire name paramKind can return, read out of the function that returns
// them.
function wireKinds(): string[] {
  const src = read('packages/agent/workspace/workspace_modelparams.go')
  const fn = src.match(/func paramKind\(k provider\.ParamKind\) string \{([\s\S]*?)\n\}/)
  if (!fn) {
    throw new Error(
      'paramKind not found in packages/agent/workspace/workspace_modelparams.go — ' +
        'the anchor moved, and this guard was silently checking nothing',
    )
  }
  return [...fn[1].matchAll(/return "([^"]+)"/g)].map((m) => m[1])
}

// The kind union on ModelParamSpec, scoped to that interface so another
// interface's `kind` field cannot answer for it.
function clientKinds(): string[] {
  const src = read('packages/agent/web/client/src/platform/ctrlproto/types.ts')
  const block = src.match(/export interface ModelParamSpec \{([\s\S]*?)\n\}/)
  if (!block) throw new Error('ModelParamSpec not found in types.ts — the anchor moved')
  const line = block[1].match(/\n\s*kind: ([^\n]+)/)
  if (!line) throw new Error('ModelParamSpec has no kind field — the anchor moved')
  return [...line[1].matchAll(/'([^']+)'/g)].map((m) => m[1])
}

// Kinds the form renders with the shared text input on purpose. A text box
// genuinely fits all three: they are a string, a number and a number. Anything
// else has to earn its widget or say why it does not need one.
const RENDERED_AS_TEXT = new Set(['text', 'int', 'float'])

describe('model param kind parity', () => {
  it('the client type knows every kind the daemon can send', () => {
    const wire = wireKinds()
    // A regex that matched nothing would make every assertion below vacuous.
    expect(wire.length, 'paramKind returned no wire names — the extraction is broken').toBeGreaterThan(3)

    const client = clientKinds()
    for (const k of wire) {
      expect(
        client,
        `the daemon can send kind '${k}' and ModelParamSpec.kind does not list it, ` +
          `so TypeScript cannot make the form handle it`,
      ).toContain(k)
    }
  })

  it('every kind is branched on, or is a deliberate text fallback', () => {
    const form = read('packages/agent/web/client/src/features/models/ModelParamsForm.tsx')
    for (const k of wireKinds()) {
      if (RENDERED_AS_TEXT.has(k)) continue
      expect(
        form,
        `the daemon can send kind '${k}' and ModelParamsForm has no branch for it, ` +
          `so it falls through to the text input — the operator gets a box and has ` +
          `to guess what to type. Add a branch, or add '${k}' to RENDERED_AS_TEXT ` +
          `with a reason a text box actually fits it.`,
      ).toContain(`p.kind === '${k}'`)
    }
  })
})
