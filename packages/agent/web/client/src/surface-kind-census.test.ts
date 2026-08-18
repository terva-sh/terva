import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import { describe, expect, it } from 'vitest'

// The daemon decides which surface kinds exist; the client's SurfaceView switch
// decides which of them can be drawn. Nothing types that agreement — SurfaceMeta.kind
// and Surface.kind are both plain `string`, so TypeScript cannot check it — and
// nothing tested it either.
//
// It had already drifted. workspace_surfaces.go emitted Kind:"characters" for
// every workspace-backed session and Kind:"memory" whenever memory was on, while
// SurfaceView had cases for neither. PaneHost renders a tab for every non-`ext:`
// surface, so both tabs appeared in the panel rail, and clicking either fell
// through fifteen cases to `default` and rendered "unsupported pane". The commit
// that added the characters library said it rode "the existing surface host (no
// new client renderer needed to reach it over the wire)" — that assumption is
// exactly what was false, which is how the tab shipped dead.
//
// This reads both sides out of the actual sources rather than restating either.
// A list typed out here would be a third copy and would drift the same way.

const repoRoot = join(__dirname, '..', '..', '..', '..', '..')

function daemonSurfaceKinds(): string[] {
  const src = readFileSync(join(repoRoot, 'packages/agent/workspace/workspace_surfaces.go'), 'utf8')
  const kinds = new Set<string>()
  for (const m of src.matchAll(/Kind:\s*"([a-z_-]+)"/g)) kinds.add(m[1])
  return [...kinds].sort()
}

function clientSurfaceCases(): string[] {
  const src = readFileSync(join(__dirname, 'app.tsx'), 'utf8')
  // Isolate SurfaceView's switch so unrelated switches in the file cannot
  // inflate the answer and mask a genuinely missing case.
  const start = src.indexOf('function SurfaceView')
  expect(start, 'SurfaceView not found in app.tsx — this census is anchored on it').toBeGreaterThan(-1)
  const body = src.slice(start)
  const end = body.indexOf("default:\n      return <div class=\"pick-empty\">{t('unsupported pane')}</div>")
  expect(end, 'SurfaceView default arm not found — the anchor moved, fix this census').toBeGreaterThan(-1)
  const cases = new Set<string>()
  for (const m of body.slice(0, end).matchAll(/^\s*case '([a-z_-]+)':/gm)) cases.add(m[1])
  return [...cases].sort()
}

describe('surface kind census', () => {
  it('reads a non-trivial number of kinds from both sides', () => {
    // Guards the guard. If either extractor silently matched nothing, every
    // assertion below would pass vacuously — which is the failure mode that let
    // two dead tabs ship.
    expect(daemonSurfaceKinds().length).toBeGreaterThan(10)
    expect(clientSurfaceCases().length).toBeGreaterThan(10)
  })

  it('every surface kind the daemon emits has a SurfaceView case', () => {
    const emitted = daemonSurfaceKinds()
    const handled = new Set(clientSurfaceCases())
    const dead = emitted.filter((k) => !handled.has(k))
    expect(
      dead,
      `workspace_surfaces.go emits these kinds with no SurfaceView case, so PaneHost renders a tab that ` +
        `draws "unsupported pane" when clicked. Add a case (and a renderer), or stop emitting the surface.`,
    ).toEqual([])
  })

  it('names memory and characters explicitly, the two that shipped dead', () => {
    // Named so a regression reads as "this exact bug came back" rather than as
    // an anonymous diff in a list.
    const handled = new Set(clientSurfaceCases())
    expect(handled.has('memory')).toBe(true)
    expect(handled.has('characters')).toBe(true)
  })
})
