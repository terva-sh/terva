// Import-boundary guard for the documented dependency direction (docs/web.md):
// platform → features/ui → composition (app.tsx). The layering is otherwise
// convention-only — package.json has no boundary lint — so this focused test
// rejects the obvious violations before they erode the modularization:
//
//   - src/platform/ is Preact-free protocol/state code: no preact, and no
//     imports from features/, ui/, or the composition roots.
//   - src/ui/ holds shared presentation primitives: it may import platform
//     (the documented exception) but never features/ or the composition roots.
//   - src/features/ may not import the composition roots.
//   - src/apps/* (the non-panel apps — Stage) may import ui/ and platform/ but
//     never features/. That layer is the PANEL's own code, extracted out of
//     app.tsx for file size — not a shared layer (a 2026-07 audit measured 0
//     Stage consumers across all of features/). A Stage module reaching for a
//     features/ helper is the signal to promote it down into ui/ or platform/,
//     which is the documented direction of sharing — not to import sideways
//     into the panel and grow a silent mirror. The panel roots (app.tsx,
//     main.tsx) are exempt: importing features/ is exactly what they are for.
//
// Deliberately a small allow/deny walk, not a lint stack; extend the rules as
// the extraction continues.
import { readdirSync, readFileSync } from 'node:fs'
import { dirname, relative, resolve } from 'node:path'
import { describe, expect, it } from 'vitest'

const SRC = resolve(__dirname)

function walk(dir: string): string[] {
  const out: string[] = []
  for (const entry of readdirSync(dir, { withFileTypes: true })) {
    const p = resolve(dir, entry.name)
    if (entry.isDirectory()) {
      if (entry.name === 'node_modules') continue
      out.push(...walk(p))
    } else if (/\.tsx?$/.test(entry.name) && !/\.test\.tsx?$/.test(entry.name)) {
      out.push(p)
    }
  }
  return out
}

// Static import/export-from/dynamic-import specifiers, plus bare side-effect
// imports. A regex is enough for this codebase's plain specifier strings.
function importsOf(file: string): string[] {
  const text = readFileSync(file, 'utf8')
  const specs: string[] = []
  const re = /(?:import|export)\s[^'"]*?from\s*['"]([^'"]+)['"]|import\s*\(\s*['"]([^'"]+)['"]\s*\)|import\s*['"]([^'"]+)['"]/g
  for (const m of text.matchAll(re)) {
    const spec = m[1] ?? m[2] ?? m[3]
    if (spec) specs.push(spec)
  }
  return specs
}

// Resolves a relative import to its src-relative path; null for packages.
function srcTarget(file: string, spec: string): string | null {
  if (!spec.startsWith('.')) return null
  return relative(SRC, resolve(dirname(file), spec)).replaceAll('\\', '/')
}

function layerOf(srcRel: string): string {
  const top = srcRel.split('/')[0]
  if (top === 'platform' || top === 'ui' || top === 'features') return top
  return 'root'
}

// The composition roots the lower layers must never import: the panel
// (app.tsx/main.tsx at the top) and every app under apps/ (the Stage app is
// apps/stage/). apps/* files themselves are classified 'root' by layerOf and so
// are free to import platform/ui/features — sharing happens by promotion
// downward, never by a lower layer reaching up into an app.
const COMPOSITION_ROOTS = /^(app|main)(\.tsx?)?$|^apps\//

// A non-panel app: apps/stage/… and any future sibling, but NOT the panel roots
// app.tsx/main.tsx. These may share downward (ui/, platform/) but must not reach
// sideways into features/ — see the header.
const NON_PANEL_APP = /^apps\//

interface Violation {
  file: string
  spec: string
  why: string
}

function violationsIn(files: string[]): Violation[] {
  const out: Violation[] = []
  for (const file of files) {
    const rel = relative(SRC, file).replaceAll('\\', '/')
    const layer = layerOf(rel)
    const inApp = NON_PANEL_APP.test(rel)
    // apps/* are 'root' by layerOf and otherwise unconstrained, but they carry
    // one rule of their own: no importing features/ (the panel's private code).
    if (layer === 'root' && !inApp) continue
    for (const spec of importsOf(file)) {
      if (layer === 'platform' && /^@?preact(\/|$)/.test(spec)) {
        out.push({ file: rel, spec, why: 'platform must stay Preact-free' })
        continue
      }
      const target = srcTarget(file, spec)
      if (target === null) continue
      const targetLayer = layerOf(target)
      if (inApp) {
        if (targetLayer === 'features') {
          out.push({
            file: rel,
            spec,
            why: 'an app must not import features/ — it is the panel\'s own code; promote the module to ui/ or platform/ first',
          })
        }
        continue
      }
      if (COMPOSITION_ROOTS.test(target)) {
        out.push({ file: rel, spec, why: `${layer} must not import the composition root` })
      } else if (layer === 'platform' && (targetLayer === 'features' || targetLayer === 'ui')) {
        out.push({ file: rel, spec, why: 'platform must not import features/ui' })
      } else if (layer === 'ui' && targetLayer === 'features') {
        out.push({ file: rel, spec, why: 'ui must not import features (platform is the only allowed lower layer)' })
      }
    }
  }
  return out
}

describe('import boundaries (docs/web.md dependency direction)', () => {
  const files = walk(SRC)

  it('sees the layered tree it guards', () => {
    // apps/ is here too: the apps→features rule silently passes if no apps/*
    // file is ever walked, so assert the subtree the rule inspects exists.
    for (const layer of ['platform/', 'ui/', 'features/', 'apps/']) {
      expect(
        files.some((f) => relative(SRC, f).replaceAll('\\', '/').startsWith(layer)),
        `no files under src/${layer} — update this guard alongside the restructure`,
      ).toBe(true)
    }
  })

  it('finds no violations', () => {
    const violations = violationsIn(files)
    expect(
      violations,
      violations.map((v) => `${v.file} imports "${v.spec}" — ${v.why}`).join('\n'),
    ).toEqual([])
  })

  it('would catch each violation class (self-test on synthetic imports)', () => {
    // Guard the guard: classify synthetic edges without touching the tree.
    const cases: Array<[string, string, boolean]> = [
      ['platform/ctrlproto/x.ts', 'preact', true],
      ['platform/ctrlproto/x.ts', 'preact/hooks', true],
      ['platform/conversation/x.ts', '../../features/y', true],
      ['platform/conversation/x.ts', '../../ui/y', true],
      ['platform/conversation/x.ts', '../../app', true],
      ['platform/conversation/x.ts', '../../apps/stage/Stage', true], // no reaching up into an app
      ['ui/x.ts', '../apps/stage/Stage', true],
      ['ui/x.ts', '../features/y', true],
      ['ui/x.ts', '../platform/ctrlproto/y', false], // the documented exception
      ['features/x.ts', '../app', true],
      ['features/x.ts', '../platform/ctrlproto/y', false],
      ['features/x.ts', '../ui/y', false],
      // apps/* (Stage): ui/ and platform/ ok, features/ forbidden, siblings ok.
      ['apps/stage/Chat.tsx', '../../features/models/ModelPicker', true],
      ['apps/stage/Chat.tsx', '../../ui/Markdown', false],
      ['apps/stage/Chat.tsx', '../../platform/conversation/session', false],
      ['apps/stage/Chat.tsx', './useConversation', false],
      // The panel roots are exempt — importing features/ is what they are for.
      ['app.tsx', './features/models/ModelPicker', false],
      ['main.tsx', './features/board/SessionsBoard', false],
    ]
    for (const [fakeRel, spec, wantViolation] of cases) {
      const target = srcTarget(resolve(SRC, fakeRel), spec)
      const layer = layerOf(fakeRel)
      const inApp = NON_PANEL_APP.test(fakeRel)
      let violates = false
      // Mirror violationsIn exactly: panel roots and non-features app siblings
      // are unconstrained here.
      if (layer === 'root' && !inApp) violates = false
      else if (layer === 'platform' && /^@?preact(\/|$)/.test(spec)) violates = true
      else if (target !== null) {
        const targetLayer = layerOf(target)
        if (inApp) violates = targetLayer === 'features'
        else
          violates =
            COMPOSITION_ROOTS.test(target) ||
            (layer === 'platform' && (targetLayer === 'features' || targetLayer === 'ui')) ||
            (layer === 'ui' && targetLayer === 'features')
      }
      expect(violates, `${fakeRel} importing "${spec}"`).toBe(wantViolation)
    }
  })
})
