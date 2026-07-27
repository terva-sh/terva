import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { dirname, join } from 'node:path'
import { describe, expect, it } from 'vitest'

// A persona write REPLACES the whole file, and the editor sends the form
// spread verbatim as PersonaWriteParams — so a wire field the form does not
// carry is a field every web edit silently deletes (the daemon's own comment
// on `extends` names this exact trap). Three hand-written lists must agree:
// the PersonaForm interface, the EMPTY literal, and formFromView's copy —
// against the wire's PersonaWriteParams. Same source-reading mechanism as
// types-shape.test.ts, one file over.
const here = dirname(fileURLToPath(import.meta.url))
const editor = readFileSync(join(here, './PersonaEditor.tsx'), 'utf8')
const typesTs = readFileSync(join(here, '../../platform/ctrlproto/types.ts'), 'utf8')

function blockKeys(src: string, opener: string): string[] {
  const start = src.indexOf(opener)
  if (start < 0) return []
  const body = src.slice(start, src.indexOf('\n}', start))
  const out: string[] = []
  for (const m of body.matchAll(/^ {2,4}(\w+)\??:/gm)) out.push(m[1])
  return [...new Set(out)].sort()
}

describe('the persona form mirrors the wire params', () => {
  const wire = blockKeys(typesTs, 'export interface PersonaWriteParams {')
  const form = blockKeys(editor, 'export interface PersonaForm {')
  const empty = blockKeys(editor, 'const EMPTY: PersonaForm = {')
  const fromView = blockKeys(editor, 'export function formFromView(')

  it('reads all four lists', () => {
    expect(wire.length).toBeGreaterThan(10)
    expect(form.length).toBeGreaterThan(10)
    expect(empty.length).toBeGreaterThan(10)
    expect(fromView.length).toBeGreaterThan(10)
  })

  it('the form carries every wire field, and invents none', () => {
    expect(form, 'PersonaForm vs PersonaWriteParams — a missing key is a field every web save deletes').toEqual(wire)
  })

  it('EMPTY and formFromView copy the whole form', () => {
    expect(empty, 'EMPTY literal vs PersonaForm').toEqual(form)
    expect(fromView, 'formFromView copy list vs PersonaForm').toEqual(form)
  })
})
