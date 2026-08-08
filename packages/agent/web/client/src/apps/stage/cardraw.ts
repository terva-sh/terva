// Writing fields back into a card's raw CCv2 document.
//
// This exists because getting it wrong is SILENT. `cards.edit` re-parses what it
// is given, so a field written at the top level of a v2 wrapper is parsed off
// and lost — the save succeeds, the panel closes as though the edit landed, and
// the card is unchanged. That shipped once already, in the card-enrich flow.
//
// The rule is the parser's own branch, not a guess: a document that declares a
// `chara_card_v*` spec carries its fields under `data`; a bare v1 object carries
// them at the top level. One implementation, one test, two callers.
export function applyCardFields(raw: unknown, edits: Record<string, string>): Record<string, unknown> {
  const doc = { ...((raw ?? {}) as Record<string, unknown>) }
  const spec = typeof doc.spec === 'string' ? doc.spec : ''
  const nested = spec.startsWith('chara_card_v') && typeof doc.data === 'object' && doc.data !== null
  const fields = nested ? { ...(doc.data as Record<string, unknown>) } : doc
  for (const [k, v] of Object.entries(edits)) fields[k] = v
  if (nested) doc.data = fields
  return doc
}
