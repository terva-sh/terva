import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { dirname, join } from 'node:path'
import { describe, expect, it } from 'vitest'

// types.ts opens by claiming it "mirrors packages/agent/ctrlproto's wire shapes;
// the Go golden-frame tests pin the exact JSON these interfaces describe." That
// claim was false: nothing in this client had ever read a Go file, and only 8 of
// 101 verbs have a golden at all. So the mirror drifted in silence — the clearest
// case being NextSceneParams.world, absent here while NextScene.tsx sent it
// anyway, which worked only because send() took `unknown`.
//
// This makes the claim true for the vocabulary: the Verb union is read against
// methods.go itself, in both directions. A verb added in Go and forgotten here
// fails; a verb removed or renamed in Go and left behind here fails too.
//
// The repo already used this shape for two small helpers (toolsummary.test.ts,
// atcomplete.test.ts) — this points it at the protocol.
const here = dirname(fileURLToPath(import.meta.url))
const methodsGo = readFileSync(join(here, '../../../../../ctrlproto/methods.go'), 'utf8')
const typesTs = readFileSync(join(here, './types.ts'), 'utf8')

// Verbs a WORKSPACE daemon does not serve, so this client — which only ever
// talks to one — must not name them.
//
// 🚨 The only entries that belong here are verbs served by a DIFFERENT server.
// The `tenants` group is the `terva serve` supervisor's, and the boundary is
// structural rather than a role check: there is no ctrlproto dispatch entry for
// these anywhere, the tenant proxy's forwardable allowlist omits the group, and
// the supervisor answers them on its own connection (proposal D8). A component
// that could type `send('tenants.suspend')` would be typing a verb the daemon at
// the other end of this socket genuinely does not have — the same phantom this
// file exists to prevent, arriving from the vocabulary side. The operator
// surface is a server-rendered page at /supervisor, not part of this bundle.
//
// Enforced in reverse below, so an entry cannot outlive its reason.
const servedElsewhere: Record<string, string> = {
  'tenants.list': 'the terva serve supervisor, on its own connection',
  'tenants.suspend': 'the terva serve supervisor, on its own connection',
  'tenants.resume': 'the terva serve supervisor, on its own connection',
}

// `MethodX Method = "verb"` — the constant block methods_complete_test.go reads.
// Verbs another server owns are excluded here rather than filtered at each
// assertion, so both directions agree about the universe.
function goVerbs(): string[] {
  const out: string[] = []
  for (const m of methodsGo.matchAll(/^\s*Method\w+\s+Method\s*=\s*"([^"]+)"/gm)) {
    if (!(m[1] in servedElsewhere)) out.push(m[1])
  }
  return [...new Set(out)].sort()
}

// allGoVerbs is the unfiltered list, for the reverse check on servedElsewhere.
function allGoVerbs(): string[] {
  const out: string[] = []
  for (const m of methodsGo.matchAll(/^\s*Method\w+\s+Method\s*=\s*"([^"]+)"/gm)) out.push(m[1])
  return [...new Set(out)].sort()
}

// The `export type Verb =` union, up to the blank line that ends it.
function tsVerbs(): string[] {
  const start = typesTs.indexOf('export type Verb =')
  if (start < 0) return []
  const body = typesTs.slice(start, typesTs.indexOf('\n\n', start))
  const out: string[] = []
  for (const m of body.matchAll(/\|\s*'([^']+)'/g)) out.push(m[1])
  return [...new Set(out)].sort()
}

describe('the Verb union mirrors ctrlproto/methods.go', () => {
  // A regex that quietly matched nothing would make every assertion below pass.
  it('reads both sides', () => {
    expect(goVerbs().length).toBeGreaterThan(90)
    expect(tsVerbs().length).toBeGreaterThan(90)
  })

  it('names every verb Go serves', () => {
    const missing = goVerbs().filter((v) => !tsVerbs().includes(v))
    expect(missing, `in methods.go but not in the Verb union: ${missing.join(', ')}`).toEqual([])
  })

  it('names no verb Go does not serve', () => {
    const extra = tsVerbs().filter((v) => !goVerbs().includes(v))
    expect(extra, `in the Verb union but not in methods.go: ${extra.join(', ')}`).toEqual([])
  })

  // The excuse list is a claim about Go, so it is checked against Go: an entry
  // for a verb that no longer exists is a stale reason nobody would notice, and
  // one whose verb this client HAS learned to send is a boundary that quietly
  // moved.
  it('excuses only verbs that exist and that this client does not send', () => {
    const gone = Object.keys(servedElsewhere).filter((v) => !allGoVerbs().includes(v))
    expect(gone, `on the servedElsewhere list but not in methods.go: ${gone.join(', ')}`).toEqual([])
    const claimed = Object.keys(servedElsewhere).filter((v) => tsVerbs().includes(v))
    expect(
      claimed,
      `the Verb union names a verb another server owns: ${claimed.join(', ')}`,
    ).toEqual([])
  })
})
