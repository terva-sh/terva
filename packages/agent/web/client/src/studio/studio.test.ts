import { existsSync, readdirSync, readFileSync } from 'node:fs'
import { describe, expect, it } from 'vitest'
import { handle, TABLE_PATH, type Req, type Res } from '../../scripts/studio-save'
import { rowsOf, TABLE, type Table } from '../features/talkoot/face/poses'
import { eyePoints, formId, formsOf, parseForm, runCheck } from './check'
import { formatTable, validTable } from './table'

// The studio's own logic, without a browser. The transition check's geometry
// and the page itself run in tests/smoke/talkoot-studio.smoke.ts.

const client = new URL('../../', import.meta.url)
const read = (path: string) => readFileSync(new URL(path, client), 'utf8')

describe('the studio stays out of the build', () => {
  it('leaves studio.html out of the build inputs and out of dist', () => {
    const input = /input:\s*\{([^}]*)\}/.exec(read('vite.config.ts'))
    expect(input, 'vite.config.ts names its build inputs').not.toBeNull()
    expect(input![1]).toContain('index.html')
    expect(input![1]).not.toContain('studio')
    expect(existsSync(new URL('dist/studio.html', client))).toBe(false)
    // The studio's title is in its source, so its absence from every built
    // asset means the code never reached dist.
    expect(read('src/studio/Studio.tsx')).toContain('Talkoot Avatar Studio')
    const assets = readdirSync(new URL('dist/assets/', client))
    expect(assets.length).toBeGreaterThan(0)
    for (const a of assets) expect(read(`dist/assets/${a}`), a).not.toContain('Talkoot Avatar Studio')
  })
})

describe('forms', () => {
  it('names every form, base forms first, and reads each name back', () => {
    const ids = formsOf(TABLE).map(formId)
    expect(ids.slice(0, 2)).toEqual(['open', 'focused'])
    expect(ids).toContain('worried, strong')
    for (const id of ids) expect(formId(parseForm(id, TABLE)!)).toBe(id)
    // The prototype's names still open the pose they named.
    expect(parseForm('brows worried', TABLE)).toEqual({ pose: 'worried', intensity: 1 })
    expect(parseForm('open, strong', TABLE)).toBeUndefined()
    expect(parseForm('dreaming', TABLE)).toBeUndefined()
  })
})

describe('the transition check', () => {
  const bodies = { circle: { eyes: 11 }, pill: { eyes: 12 } }
  it('finds nothing in the committed table when every point is inside', () => {
    expect(runCheck(TABLE, bodies, () => true)).toEqual([])
  })

  it('measures a clip by the outline of each eye part, not its centre line', () => {
    // A bare slit, with no pupil: its rounded ends keep its length, and its
    // width reaches W / 2 to each side.
    const open = TABLE.poses.open.forms[0]
    const bare: Table = { ...TABLE, poses: { ...TABLE.poses, open: { forms: [{ ...open, l: { ...open.l, pupilScale: 0 } }] } } }
    const rows = rowsOf('open', 0, bare)
    const xs = (pts: [number, number][]) => pts.map(([x]) => x)
    const ys = (pts: [number, number][]) => pts.map(([, y]) => y)
    const [centre] = eyePoints(rows, { eyes: 11 }, bare, false)
    const [outline] = eyePoints(rows, { eyes: 11 }, bare)
    expect(Math.min(...xs(centre)) - Math.min(...xs(outline))).toBeCloseTo(bare.eye.W / 2, 6)
    expect(Math.max(...ys(outline))).toBeCloseTo(Math.max(...ys(centre)), 6)
    // A wall a hair outside the centre line: the centre line clears it, and
    // the slit's width does not.
    const wall = Math.min(...xs(centre)) - 0.1
    const clips = runCheck(bare, { circle: { eyes: 11 } }, (_b, x) => x >= wall)
    expect(clips.some((f) => f.kind === 'clip' && f.from === 'open' && f.to === '')).toBe(true)
  })

  it('draws a slit shorter than it is wide as an ellipse, with no point outside it', () => {
    // At slitLen 0.2 the slit is 1.04 tall and 1.55 wide, so SVG draws an
    // ellipse. Every sampled point lies on it, not on a box around it.
    const open = TABLE.poses.open.forms[0]
    const short: Table = { ...TABLE, poses: { ...TABLE.poses, open: { forms: [{ ...open, l: { ...open.l, slitLen: 0.2, pupilScale: 0 } }] } } }
    const rows = rowsOf('open', 0, short)
    const [pts] = eyePoints(rows, { eyes: 11 }, short)
    const a = short.eye.W / 2
    const b = (short.eye.H * 0.2) / 2
    const cx = 12 - short.eye.dx
    for (const [x, y] of pts) expect(((x - cx) / a) ** 2 + ((y - 11) / b) ** 2).toBeLessThanOrEqual(1 + 1e-9)
  })

  it('finds a spin in one eye while the other holds still', () => {
    // The right eye turns 40 degrees and stays open, and the left does not
    // move. Averaged over both eyes that is 20, under the limit, which is the
    // fault this guards against.
    const row = TABLE.poses.open.forms[0].l
    const t: Table = {
      ...TABLE,
      turnLimit: 180,
      poses: { ...TABLE.poses, lean: { forms: [{ l: row, r: { ...row, tilt: 40 } }] } },
    }
    expect(runCheck(t, bodies, () => true).some((f) => f.kind === 'spin' && f.from === 'open' && f.to === 'lean')).toBe(true)
  })

  it('finds a spin when the shut rule is off, and a clip when a body has no room', () => {
    const off: Table = { ...TABLE, turnLimit: 180 }
    expect(runCheck(off, bodies, () => true).some((f) => f.kind === 'spin' && f.from === 'closed' && f.to === 'open')).toBe(true)
    const clips = runCheck(TABLE, bodies, (body) => body !== 'pill').filter((f) => f.kind === 'clip')
    expect(clips.length).toBeGreaterThan(0)
    expect(clips.every((f) => f.body === 'pill')).toBe(true)
  })
})

describe('the table file', () => {
  it('reads the committed table as valid, and writes it back byte for byte', () => {
    expect(validTable(TABLE)).toBeNull()
    expect(formatTable(TABLE)).toBe(read('src/features/talkoot/face/poses.json'))
  })

  it('refuses a table the renderer cannot read', () => {
    const bad = (t: unknown) => validTable(t)
    expect(bad({ ...TABLE, extra: 1 })).toMatch(/unknown key "extra"/)
    expect(bad({ ...TABLE, turnLimit: 360 })).toMatch(/turnLimit/)
    expect(bad({ ...TABLE, poses: { closed: TABLE.poses.closed } })).toMatch(/no open pose/)
    expect(bad({ ...TABLE, poses: { ...TABLE.poses, 'Bad Name': TABLE.poses.open } })).toMatch(/pose name/)
    expect(bad({ ...TABLE, poses: { ...TABLE.poses, open: { forms: [{ l: { slitLen: 'x' }, r: null }] } } })).toMatch(/is not a number|unknown key/)
    expect(bad({ ...TABLE, look: { outline: 1 } })).toMatch(/look\.edge/)
  })
})

describe('Save', () => {
  const req = (body: string, headers: Record<string, string>, method = 'POST'): Req => ({
    method,
    headers: { host: '127.0.0.1:4174', ...headers },
    async *[Symbol.asyncIterator]() {
      yield new TextEncoder().encode(body)
    },
  })
  const res = () => {
    const r = { statusCode: 0, body: '', setHeader: () => r, end: (b: string) => void (r.body = b) }
    return r as Res & { body: string }
  }
  const json = { 'content-type': 'application/json' }
  const text = formatTable(TABLE)

  it('writes a valid table from this origin, formatted as committed', async () => {
    const written: string[] = []
    const r = res()
    await handle(req(JSON.stringify(TABLE), { ...json, origin: 'http://127.0.0.1:4174' }), r, async (t) => void written.push(t))
    expect(r.statusCode).toBe(200)
    expect(written).toEqual([text])
  })

  it('refuses another method, another origin, a form post, and a bad table, and writes nothing', async () => {
    const cases: [Req, number][] = [
      [req(text, json, 'PUT'), 405],
      [req(text, { ...json, origin: 'http://elsewhere.example' }), 403],
      [req(text, { ...json, origin: 'not a url' }), 403],
      [req(text, { 'content-type': 'text/plain' }), 415],
      [req('{', json), 400],
      [req(JSON.stringify({ ...TABLE, poses: {} }), json), 422],
    ]
    for (const [q, status] of cases) {
      const r = res()
      await handle(q, r, async () => {
        throw new Error('wrote')
      })
      expect(r.statusCode, r.body).toBe(status)
    }
  })

  it('names the file it writes, for a test that must not save into another checkout', async () => {
    const r = res()
    await handle(req('', {}, 'GET'), r)
    expect(r.statusCode).toBe(200)
    expect(r.body).toBe(TABLE_PATH)
    expect(TABLE_PATH.endsWith('/src/features/talkoot/face/poses.json')).toBe(true)
  })

  it('refuses a body over 1 MiB', async () => {
    const r = res()
    await handle(req('x'.repeat((1 << 20) + 1), json), r, async () => {
      throw new Error('wrote')
    })
    expect(r.statusCode).toBe(413)
  })
})
