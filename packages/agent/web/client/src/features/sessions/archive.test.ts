// The archive rules, asserted where they live.
//
// The behaviour that matters here is a NEGATIVE — archiving must not confirm —
// and a negative is exactly what a component test tends to pass by accident (a
// stubbed window.confirm returning true makes a confirming implementation look
// identical to a non-confirming one). So the rule is asserted in the source, the
// way boundaries.test.ts asserts the layering: it is convention-only otherwise,
// and convention-only boundaries are the ones that drift.
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { describe, expect, it } from 'vitest'

const SRC = resolve(__dirname, '../..')
const read = (p: string) => readFileSync(resolve(SRC, p), 'utf8')

// callBody returns the source of the arrow function assigned to `name`, ending
// at its own closing brace. Bounded deliberately: a fixed-size window runs into
// the NEXT function, and a confirm belonging to a neighbour would satisfy — or
// break — an assertion about this one.
function callBody(src: string, name: string): string {
  const at = src.indexOf(`const ${name} = `)
  expect(at, `${name} not found`).toBeGreaterThan(-1)
  const end = src.indexOf('\n  }', at)
  expect(end, `${name} has no closing brace at function indentation`).toBeGreaterThan(at)
  return src.slice(at, end)
}

describe('archiving does not ask, deleting does', () => {
  // Archive is reversible: the transcript moves into a compressed subdirectory
  // and `Restore` brings it back. Confirming a reversible act is how people are
  // trained to click through the confirm on the irreversible one two rows down.
  it('the panel archives without a confirm and deletes with one', () => {
    const app = read('app.tsx')
    expect(callBody(app, 'archive')).toContain("'sessions.archive'")
    expect(callBody(app, 'archive')).not.toContain('window.confirm')
    expect(callBody(app, 'del')).toContain('window.confirm')
  })

  it('Stage archives without a confirm and deletes with one', () => {
    const lib = read('apps/stage/Library.tsx')
    expect(callBody(lib, 'archiveSession')).toContain("'sessions.archive'")
    expect(callBody(lib, 'archiveSession')).not.toContain('window.confirm')
    expect(callBody(lib, 'deleteSession')).toContain('window.confirm')
  })

  // Restore addresses an archived session by id in PARAMS. The frame's sess
  // field is a live handle that subscriptions and turn routing key on, and an
  // archived id resolves to no live session — sending it there would address
  // something that does not exist.
  it('restore passes the id as params, not as the frame session', () => {
    const body = callBody(read('app.tsx'), 'restore')
    expect(body).toMatch(/'sessions\.restore',\s*\{\s*id\s*\}/)
  })

  // The archive is fetched when it is opened, not on every drawer open: it is
  // something you go looking for, and a list nobody asked for is a round trip
  // nobody asked for.
  it('the archive list is fetched lazily, from the toggle', () => {
    const body = callBody(read('app.tsx'), 'toggleArchived')
    expect(body).toContain('loadArchived')
  })
})

describe('archive drawer labelling', () => {
  // Before the first fetch the count is unknown, and "Archived (0)" would be a
  // claim the user acts on — they would not open it.
  it('claims no count until the archive has been fetched', async () => {
    const { archiveLabel } = await import('./SessionPicker')
    expect(archiveLabel(null)).toBe('Archived')
    expect(archiveLabel(undefined)).toBe('Archived')
    expect(archiveLabel([])).toContain('0')
  })

  it('reports compressed sizes in units a human reads', async () => {
    const { humanBytes } = await import('./SessionPicker')
    expect(humanBytes(512)).toBe('512B')
    expect(humanBytes(2048)).toBe('2K')
    expect(humanBytes(5 * 1024 * 1024)).toBe('5.0M')
  })
})
