// The transition check: every move between two poses on every body, sampled
// through the move as the renderer plays it, and each pose at the far end of a
// glance. It is pure. The studio passes inside(), which asks the browser
// whether a point lies in a body, and a smoke test runs it as a gate.

import { beatStages, maxTurn, needsShut, rowsAt, rowsOf, turnOf, type Rows, type Table } from '../features/talkoot/face/poses'

// A FormRef names one form of a pose: the pose, and its intensity.
export interface FormRef {
  pose: string
  intensity: number
}

// formId is how the studio names a form: the pose, and "strong" for the form
// above it.
export const formId = (f: FormRef): string => (f.intensity === 0 ? f.pose : f.intensity === 1 ? `${f.pose}, strong` : `${f.pose}, strong ${f.intensity}`)

// formsOf lists every form of every pose: the base forms first, in the table's
// order, then the strong ones.
export function formsOf(t: Table): FormRef[] {
  const out: FormRef[] = []
  const names = Object.keys(t.poses)
  for (const pose of names) out.push({ pose, intensity: 0 })
  for (const pose of names) for (let i = 1; i < t.poses[pose].forms.length; i++) out.push({ pose, intensity: i })
  return out
}

// parseForm reads a form id back, and the prototype's "brows pose" names too,
// so its links still open the pose they named.
export function parseForm(id: string, t: Table): FormRef | undefined {
  const brows = /^brows (.+)$/.exec(id)
  const strong = /^(.+), strong(?: (\d+))?$/.exec(id)
  const ref: FormRef = brows
    ? { pose: brows[1], intensity: 1 }
    : strong
      ? { pose: strong[1], intensity: strong[2] ? Number(strong[2]) : 1 }
      : { pose: id, intensity: 0 }
  const forms = t.poses[ref.pose]?.forms
  return forms && ref.intensity < forms.length ? ref : undefined
}

export const rowsOfForm = (f: FormRef, t: Table): Rows => rowsOf(f.pose, f.intensity, t)

// easeInOut is CSS ease-in-out, cubic-bezier(.42, 0, .58, 1), solved for x by
// bisection, so a frame shows what the browser draws at that moment.
export function easeInOut(x: number): number {
  const bz = (t: number, p1: number, p2: number) => 3 * (1 - t) ** 2 * t * p1 + 3 * (1 - t) * t * t * p2 + t ** 3
  let lo = 0
  let hi = 1
  for (let i = 0; i < 24; i++) {
    const m = (lo + hi) / 2
    if (bz(m, 0.42, 0.58) < x) lo = m
    else hi = m
  }
  return bz((lo + hi) / 2, 0, 1)
}

// frameAt is what a move from a to b shows at t in [0, 1], eased as each
// stage eases. A move that shuts spends its first part shutting.
export function frameAt(a: Rows, b: Rows, t: number, table: Table): Rows {
  if (!needsShut(a, b, table)) return rowsAt(a, b, easeInOut(t), table)
  const share = 0.4
  const k = t < share ? easeInOut(t / share) * share : share + easeInOut((t - share) / (1 - share)) * (1 - share)
  return rowsAt(a, b, k, table)
}

// glanceRows is a pose at the far end of a glance: the stage of the glance
// beat that moves the eyes furthest.
export function glanceRows(rows: Rows, toward: number, table: Table): Rows {
  const stages = beatStages({ name: 'glance', key: 0, toward }, rows, rows, table)
  if (!stages.length) return rows
  return stages.reduce((a, b) => (Math.abs(b.rows[0].eyeX) > Math.abs(a.rows[0].eyeX) ? b : a)).rows
}

// A BodyGeometry is where a body's eyes sit.
export interface BodyGeometry {
  eyes: number
  cx?: number
}

type Pt = [number, number]

const turn = ([x, y]: Pt, rad: number): Pt => [x * Math.cos(rad) - y * Math.sin(rad), x * Math.sin(rad) + y * Math.cos(rad)]

// ROUND is how many points sample each circle of a part's outline. The check
// asks the browser about every point on every body at every step of every
// move, so each point here costs about 28,000 questions.
const ROUND = 8

// rounded samples the outline of an SVG rect in its own frame, centred on the
// origin, with rx of half its width, as the slit and the pupil bars are drawn.
// SVG caps a corner's vertical radius at half the rect's height, so a part
// shorter than it is wide draws as an ellipse, and each end is an ellipse
// with radii rx and ry. The ends, and the middle of a long part such as the
// slit, where a body that is not convex could reach in.
function rounded(width: number, height: number, out: (p: Pt) => void, middle: boolean) {
  const rx = width / 2
  const ry = Math.min(rx, height / 2)
  const half = height / 2 - ry
  for (const cy of middle ? [-half, 0, half] : [-half, half])
    for (let k = 0; k < ROUND; k++) {
      const q = (2 * Math.PI * k) / ROUND
      out([rx * Math.cos(q), cy + ry * Math.sin(q)])
    }
}

// eyePoints are points of both eyes in the 24 by 24 box, as the renderer
// places them. With outline, the default, they sample the outline of the slit
// and of each pupil bar, so an eye that overhangs a body by part of its width
// is a clip. Without it they are the centre lines' ends only, the prototype's
// points, which the touch test measures between.
export function eyePoints(rows: Rows, body: BodyGeometry, table: Table, outline = true): Pt[][] {
  const E = table.eye
  return rows.map((r, i) => {
    const pts: Pt[] = []
    const ox = (body.cx ?? 12) + (i === 0 ? -E.dx : E.dx) + r.eyeX
    const oy = body.eyes + r.eyeY
    const th = (turnOf(r) * Math.PI) / 180
    const put = (p: Pt) => {
      const [x, y] = turn(p, th)
      pts.push([ox + x, oy + y])
    }
    const h = (E.H * r.slitLen) / 2
    if (r.slitLen > 0.08) {
      if (outline) rounded(E.W, 2 * h, put, true)
      else {
        put([0, -h])
        put([0, h])
      }
    }
    if (r.pupilScale > 0.08) {
      const travel = (E.H / 2) * Math.max(r.slitLen, 0.35) - 0.6
      const py = -r.pupilPos * travel
      const pr = (r.pupilRot * Math.PI) / 180
      const a = (r.pupilAngle * Math.PI) / 180
      for (const j of [0, 1]) {
        const sx = (j === 0 ? -1 : 1) * Math.sin(a) * (E.L / 2) * r.spread
        const ang = (j === 0 ? 1 : -1) * a
        // A point on the bar, turned by the bar's angle, moved apart, then
        // turned by pupilRot, scaled, and moved along the slit.
        const place = (p: Pt) => {
          const [bx, by] = turn(p, ang)
          const [x, y] = turn([bx + sx, by], pr)
          put([x * r.pupilScale, py + y * r.pupilScale])
        }
        if (outline) rounded(E.BW, E.L, place, false)
        else for (const e of [-1, 1]) place([0, (e * E.L) / 2])
      }
    }
    return pts
  })
}

// A Finding is one fault. spin: a visible slit turns more than 30 degrees.
// vanish: both eyes nearly disappear mid-move without a shut. clip: part of
// an eye, counting its width, leaves the body. touch: the two eyes nearly meet. to is empty for a pose
// that clips while it is held, and "glance left" or "glance right" for a pose
// at the end of a glance.
export interface Finding {
  kind: 'spin' | 'vanish' | 'clip' | 'touch'
  from: string
  to: string
  body: string
  t: number
}

export type Inside = (body: string, x: number, y: number) => boolean

const SAMPLES = 13

export function runCheck(table: Table, bodies: Record<string, BodyGeometry>, inside: Inside): Finding[] {
  const out: Finding[] = []
  const add = (kind: Finding['kind'], from: string, to: string, body: string, t: number) => out.push({ kind, from, to, body, t })
  const forms = formsOf(table)
  const names = Object.keys(bodies)
  // The eyes sit at the same place on every body, so one body is enough to
  // find two eyes that touch.
  const touchBody = bodies.circle ? 'circle' : names[0]
  const outside = (rows: Rows, body: string) => eyePoints(rows, bodies[body], table).flat().some(([x, y]) => !inside(body, x, y))
  // A pose that clips on its own is one finding, not one for every move in or
  // out of it.
  const alone = new Set<string>()
  for (const f of forms)
    for (const body of names)
      if (outside(rowsOfForm(f, table), body)) {
        alone.add(formId(f) + '|' + body)
        add('clip', formId(f), '', body, 1)
      }
  for (const fa of forms)
    for (const fb of forms) {
      const from = formId(fa)
      const to = formId(fb)
      if (from === to) continue
      const a = rowsOfForm(fa, table)
      const b = rowsOfForm(fb, table)
      const shutPath = needsShut(a, b, table)
      const ends = [a, b].map((rs) => Math.max(...rs.map((r) => Math.max(r.slitLen, r.pupilScale))))
      // Each eye's own visible turn. One eye that spins is a spin, whatever the
      // other does, so the eyes are not averaged.
      const spin = [0, 0]
      let vanish = false
      let touch = false
      let prev = a
      for (let k = 0; k <= SAMPLES; k++) {
        const t = k / SAMPLES
        const rs = frameAt(a, b, t, table)
        if (k > 0)
          for (const [i, r] of rs.entries()) if (Math.min(r.slitLen, prev[i].slitLen) > 0.3) spin[i] += Math.abs(turnOf(r) - turnOf(prev[i]))
        prev = rs
        if (!shutPath && t > 0.2 && t < 0.8 && Math.min(...ends) > 0.3 && Math.max(...rs.map((r) => Math.max(r.slitLen, r.pupilScale))) < 0.15) vanish = true
        for (const body of names) {
          const [lp, rp] = eyePoints(rs, bodies[body], table)
          if (body === touchBody && !touch) {
            const [lc, rc] = eyePoints(rs, bodies[body], table, false)
            touch = lc.some(([x1, y1]) => rc.some(([x2, y2]) => Math.hypot(x1 - x2, y1 - y2) < 0.5))
          }
          if (alone.has(from + '|' + body) || alone.has(to + '|' + body)) continue
          if ([...lp, ...rp].some(([x, y]) => !inside(body, x, y)) && !out.some((x) => x.kind === 'clip' && x.from === from && x.to === to && x.body === body))
            add('clip', from, to, body, t)
        }
      }
      if (Math.max(...spin) > 30) add('spin', from, to, '', 0)
      if (vanish) add('vanish', from, to, '', 0)
      if (touch) add('touch', from, to, '', 0)
    }
  for (const f of forms)
    for (const dir of [-1, 1]) {
      const rs = glanceRows(rowsOfForm(f, table), dir, table)
      for (const body of names) if (outside(rs, body)) add('clip', formId(f), dir < 0 ? 'glance left' : 'glance right', body, 1)
    }
  return out
}

// summary counts the moves, the moves that shut, and each kind of finding.
export function summary(table: Table, found: Finding[]): string {
  const forms = formsOf(table)
  const pairs = forms.length * (forms.length - 1)
  let shuts = 0
  let widest = 0
  for (const a of forms)
    for (const b of forms) {
      if (a === b) continue
      const ra = rowsOfForm(a, table)
      const rb = rowsOfForm(b, table)
      if (needsShut(ra, rb, table)) shuts++
      else widest = Math.max(widest, maxTurn(ra, rb))
    }
  const count = (k: Finding['kind']) => found.filter((x) => x.kind === k).length
  return [
    `${pairs} moves, ${shuts} shut to turn`,
    `widest turn without a shut ${Math.round(widest)}°`,
    '',
    `spin    ${count('spin')}`,
    `vanish  ${count('vanish')}`,
    `clip    ${count('clip')}`,
    `touch   ${count('touch')}`,
  ].join('\n')
}

// clipKey names a clip for the agreed list: the move, and the body.
export const clipKey = (f: Pick<Finding, 'from' | 'to' | 'body'>): string => `${f.from}${f.to ? ' -> ' + f.to : ', held'} on ${f.body}`
