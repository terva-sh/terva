// Shell-style Tab completion for the composer's @-token — the TypeScript
// twin of Go's widgets.AtComplete (packages/agent/modes/widgets/
// at_complete.go). Both implementations are pinned to the same golden
// fixtures (at_complete_golden.json), so the semantics cannot drift:
// segment-wise prefix completion within the token's parent, dot-names
// hidden unless asked for, a unique directory gains "/" (token stays live),
// several candidates extend to their longest common prefix, and a base
// already at that prefix is a no-op. Tab never commits — Enter does.
import type { WireFileEntry } from '../../platform/ctrlproto/types'

export function atComplete(entries: WireFileEntry[], query: string): [string, number] {
  const slash = query.lastIndexOf('/')
  const parent = slash >= 0 ? query.slice(0, slash + 1) : ''
  const base = slash >= 0 ? query.slice(slash + 1) : query

  const seen = new Set<string>()
  const kids: { name: string; dir: boolean }[] = []
  for (const e of entries) {
    if (!e.path.startsWith(parent)) continue
    const rest = e.path.slice(parent.length)
    if (!rest || rest.includes('/')) continue
    if (!rest.startsWith(base)) continue
    if (rest.startsWith('.') && !base.startsWith('.')) continue
    if (seen.has(rest)) continue
    seen.add(rest)
    kids.push({ name: rest, dir: !!e.dir })
  }
  if (kids.length === 0) return [query, 0]
  if (kids.length === 1) return [parent + kids[0].name + (kids[0].dir ? '/' : ''), 1]

  const names = kids.map((k) => k.name).sort()
  let lcp = names[0]
  for (const n of names) {
    let i = 0
    while (i < lcp.length && i < n.length && lcp[i] === n[i]) i++
    lcp = lcp.slice(0, i)
  }
  if (lcp === base) return [query, kids.length]
  return [parent + lcp, kids.length]
}
