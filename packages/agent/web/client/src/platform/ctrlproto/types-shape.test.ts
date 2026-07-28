import { readFileSync, readdirSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { dirname, join } from 'node:path'
import { describe, expect, it } from 'vitest'

// verbs.test.ts made types.ts's "mirrors ctrlproto's wire shapes" claim true for
// the VOCABULARY — its own header says so, and says no more. Shapes still
// drifted in silence: TaskBoardView, TaskBoardItem and Surface.task_board sat
// unmirrored for several releases, noticed only when someone built the panel
// that needed them. A view field added in Go and forgotten here doesn't fail a
// build or a test — the client just never renders it, which reads as "the
// feature doesn't exist" rather than "the mirror is stale".
//
// This makes the claim true for the shapes, by the same means: read the Go
// source itself, compare both directions.
//
//   - every wire struct in ctrlproto's four wire files must be mirrored here
//     (same name, or via the rename map below), or carry a reason on the
//     not-mirrored list — a NEW Go type cannot ship silently unmirrored
//   - for every mirrored pair, the field sets must match exactly: a Go json
//     tag absent here is a stale mirror; a field here that Go doesn't serve is
//     a phantom the compiler will happily let components read forever
//     (NextSceneParams.world shipped exactly that way, in the other direction)
//   - enum domains stated on both sides are compared as sets, not prose: the
//     workflow run status union below must equal the Status constants in
//     workflow/runs/record.go, which widened once already (running/crashed)
const here = dirname(fileURLToPath(import.meta.url))
const ctrlprotoDir = join(here, '../../../../../ctrlproto')
// The whole package is the wire surface — a new .go file joins this universe
// by existing, so a type in it cannot dodge the accounting below.
const goWireFiles = readdirSync(ctrlprotoDir)
  .filter((f) => f.endsWith('.go') && !f.endsWith('_test.go'))
  .map((f) => readFileSync(join(ctrlprotoDir, f), 'utf8'))
// ctrlproto.Event embeds core.WireEvent (its payload fields flatten into the
// same JSON object), so the embed source is parsed too — for resolution only,
// not as part of the must-be-mirrored universe.
const coreWireGo = readFileSync(join(here, '../../../../../../core/wire.go'), 'utf8')
const recordGo = readFileSync(join(here, '../../../../../workflow/runs/record.go'), 'utf8')
const typesTs = readFileSync(join(here, './types.ts'), 'utf8')

// Go wire types mirrored under a different TS name. Keep this to renames that
// exist today; a new type should just use the Go name.
const renamed: Record<string, string> = {
  Event: 'WireEvent',
  FileEntry: 'WireFileEntry',
  GroupView: 'Group',
  Hello: 'ServerHello',
  ListItem: 'ListItemW',
  PermissionRule: 'PermissionRuleInfo',
  UsageWindowInfo: 'UsageWindow',
}

// Request/response envelopes are typed at the call site — the client writes
// send<{ sessions: SessionInfo[] }>('sessions.list', …) and passes params as
// literals, so *Params/*Result envelopes have no named mirror by convention.
// The VIEW types they carry are what this file declares, and what the field
// check below verifies. An envelope the client does name (CardGroupSaveParams,
// RealizeParams, …) is still field-checked like any other shared type.
const envelope = /(?:Params|Result)$/

// Fields a mirrored type carries deliberately BEYOND the wire — client-side
// state living on the wire shape. Each is documented at its declaration; a
// field listed here that disappears from types.ts fails the honesty check.
const clientOnly: Record<string, string[]> = {
  // system marks a client-SYNTHESIZED group derived from session metadata;
  // never sent to the daemon, it only gates the UI.
  Group: ['system'],
}

// Go wire types with no TS mirror, each with the reason. An entry here is a
// claim the test enforces in reverse too: if the type gains a mirror, the
// entry must go.
const notMirrored: Record<string, string> = {
  Answer: 'ask answers are sent as literals, like every request payload',
  AttachmentRef: 'a bare staged-file id, sent inline inside prompt params (like Image)',
  AuthFlowRef: 'a single-field flow handle, echoed back inside auth params',
  Error: 'mirrored inline as Frame.error',
  Image: 'a prompt attachment, sent inline inside prompt params',
  RealizeWorld: 'mirrored inline as RealizeProposal.world',
  Resolved: 'mirrored inline as WireEvent.resolved',
  SideChatTurn: 'side chat is a TUI-only surface today; no web caller names it',
  UserPersonaRef: 'a single-field ref, sent inline inside userpersonas params',
}

// ---- Go side: struct name -> sorted wire field names (json tags) ----
// An embedded struct (`core.WireEvent` on a line of its own) flattens its
// fields into the embedder's JSON object, so embeds are resolved by union.
// An embed this parser cannot resolve fails loudly in 'reads both sides'
// rather than silently under-comparing the type.
const unresolvedEmbeds: string[] = []

function parseStructs(src: string, embeds?: Map<string, string[]>): Map<string, string[]> {
  const out = new Map<string, string[]>()
  for (const m of src.matchAll(/^type ([A-Z]\w*) struct \{\n([\s\S]*?)^\}/gm)) {
    const fields: string[] = []
    for (const f of m[2].matchAll(/`json:"([^",]+)[^"]*"`/g)) {
      if (f[1] !== '-') fields.push(f[1])
    }
    const found = [...m[2].matchAll(/^\t(?:\w+\.)?([A-Z]\w*)$/gm)].map((e) => e[1])
    if (embeds && found.length) embeds.set(m[1], found)
    // A struct with no json-tagged field and no embed is not a wire shape —
    // ctrlproto.Contract (untagged, in-process only) is the case in point.
    if (fields.length || found.length) out.set(m[1], fields.sort())
  }
  return out
}

function goStructs(): Map<string, string[]> {
  const embeds = new Map<string, string[]>()
  const out = new Map<string, string[]>()
  for (const src of goWireFiles) {
    for (const [name, fields] of parseStructs(src, embeds)) out.set(name, fields)
  }
  const coreStructs = parseStructs(coreWireGo)
  for (const [name, embedded] of embeds) {
    const fields = new Set(out.get(name))
    for (const e of embedded) {
      const donor = out.get(e) ?? coreStructs.get(e)
      if (!donor) {
        unresolvedEmbeds.push(`${name} embeds ${e}`)
        continue
      }
      for (const f of donor) fields.add(f)
    }
    out.set(name, [...fields].sort())
  }
  return out
}

// ---- TS side: interface/object-alias name -> { fields, parent } ----
// Line-based with brace depth: fields are collected at depth 1 only, so a
// nested object literal contributes nothing, and `extends` is resolved one
// level (the two uses today are one level deep).
function tsShapes(): Map<string, { fields: string[]; parent?: string }> {
  const out = new Map<string, { fields: string[]; parent?: string }>()
  const lines = typesTs.split('\n')
  for (let i = 0; i < lines.length; i++) {
    const open = lines[i].match(
      /^export (?:interface (\w+)(?: extends (\w+))? \{|type (\w+) = \{)$/,
    )
    if (!open) continue
    const name = open[1] ?? open[3]
    const parent = open[2]
    const fields: string[] = []
    let depth = 1
    for (i++; i < lines.length && depth > 0; i++) {
      const line = lines[i]
      const trimmed = line.trim()
      if (trimmed.startsWith('//') || trimmed.startsWith('*') || trimmed.startsWith('/*')) continue
      if (depth === 1) {
        const field = line.match(/^ {2}(\w+)\??:/)
        if (field) fields.push(field[1])
      }
      depth += (line.match(/\{/g) ?? []).length - (line.match(/\}/g) ?? []).length
    }
    i--
    out.set(name, { fields: fields.sort(), parent })
  }
  return out
}

function tsEffectiveFields(shapes: ReturnType<typeof tsShapes>, name: string): string[] {
  const shape = shapes.get(name)
  if (!shape) return []
  const inherited = shape.parent ? (shapes.get(shape.parent)?.fields ?? []) : []
  return [...new Set([...shape.fields, ...inherited])].sort()
}

describe('the wire-shape mirror stays whole', () => {
  const go = goStructs()
  const ts = tsShapes()

  // A regex that quietly matched nothing would make every assertion below pass.
  it('reads both sides', () => {
    expect(go.size).toBeGreaterThan(100)
    expect(ts.size).toBeGreaterThan(120)
    expect([...go.keys()].filter((n) => ts.has(n)).length).toBeGreaterThan(60)
    expect(unresolvedEmbeds, unresolvedEmbeds.join(', ')).toEqual([])
  })

  it('accounts for every Go wire type: mirrored, renamed, an envelope, or excused', () => {
    const unaccounted = [...go.keys()].filter(
      (n) => !ts.has(n) && !(n in renamed) && !(n in notMirrored) && !envelope.test(n),
    )
    expect(
      unaccounted,
      `Go wire types with no TS mirror and no entry on the lists above: ${unaccounted.join(', ')}`,
    ).toEqual([])
  })

  it('keeps the excuse lists honest', () => {
    const mirroredAnyway = Object.keys(notMirrored).filter((n) => ts.has(n))
    expect(
      mirroredAnyway,
      `on the not-mirrored list but present in types.ts — delete the entries: ${mirroredAnyway.join(', ')}`,
    ).toEqual([])
    const renamedGone = Object.entries(renamed)
      .filter(([g, t]) => !go.has(g) || !ts.has(t))
      .map(([g, t]) => `${g}→${t}`)
    expect(
      renamedGone,
      `rename map entries whose ends no longer exist: ${renamedGone.join(', ')}`,
    ).toEqual([])
    const redundant = Object.keys(notMirrored).filter((n) => n in renamed || envelope.test(n))
    expect(
      redundant,
      `already covered by the rename map or the envelope rule: ${redundant.join(', ')}`,
    ).toEqual([])
    const staleClientOnly = Object.entries(clientOnly)
      .flatMap(([t, fs]) => fs.map((f) => `${t}.${f}`))
      .filter((tf) => {
        const [t, f] = tf.split('.')
        return !tsEffectiveFields(ts, t).includes(f)
      })
    expect(
      staleClientOnly,
      `client-only exemptions naming fields types.ts no longer has: ${staleClientOnly.join(', ')}`,
    ).toEqual([])
  })

  it('mirrors every field of every mirrored type, and invents none', () => {
    const problems: string[] = []
    for (const [name, goFields] of go) {
      const tsName = name in renamed ? renamed[name] : ts.has(name) ? name : null
      if (!tsName) continue
      const tsFields = tsEffectiveFields(ts, tsName)
      const missing = goFields.filter((f) => !tsFields.includes(f))
      const phantom = tsFields.filter(
        (f) => !goFields.includes(f) && !clientOnly[tsName]?.includes(f),
      )
      if (missing.length) problems.push(`${tsName} lacks Go's: ${missing.join(', ')}`)
      if (phantom.length) problems.push(`${tsName} has fields Go doesn't serve: ${phantom.join(', ')}`)
    }
    expect(problems, problems.join('\n')).toEqual([])
  })

  it('agrees with workflow/runs on the run status domain', () => {
    const goDomain = [...recordGo.matchAll(/^\tStatus\w+\s+Status = "(\w+)"/gm)]
      .map((m) => m[1])
      .sort()
    // The status field's union inside WorkflowRunInfo, wherever it wraps.
    const block = typesTs.slice(typesTs.indexOf('export interface WorkflowRunInfo'))
    const statusLine = block.match(/^ {2}status\??:([^\n]*(?:\n {4}[^\n]*)*)/m)
    const tsDomain = [...(statusLine?.[1] ?? '').matchAll(/'(\w+)'/g)].map((m) => m[1]).sort()
    expect(goDomain.length).toBeGreaterThan(3)
    expect(tsDomain, 'WorkflowRunInfo.status union vs runs/record.go Status constants').toEqual(
      goDomain,
    )
  })
})
