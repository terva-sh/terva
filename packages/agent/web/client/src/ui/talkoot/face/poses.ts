// The face's pose table and the rules that move between poses
// (docs/proposals/talkoot-members.md, "The face"). The numbers live in
// poses.json, which the Talkoot Avatar Studio edits. This file reads them.

import type { TalkootMemberStatus } from '../../../platform/ctrlproto/types'
import table from './poses.json'

// A Row places one eye. Each number moves, scales, or turns one part of it.
export interface Row {
  slitLen: number
  slitRot: number
  eyeX: number
  eyeY: number
  pupilPos: number
  pupilScale: number
  tilt: number
  spread: number
  pupilAngle: number
  pupilRot: number
}

// Rows are the two eyes, left then right.
export type Rows = [Row, Row]

// A Form is one intensity of a pose. r null mirrors l.
export interface Form {
  l: Partial<Row>
  r: Partial<Row> | null
  loop?: string
}

export interface BeatStep {
  row?: Partial<Row>
  set?: Partial<Row>
  min?: Partial<Row>
  ms: number
  hold: number
}

// A Table is everything that tunes the face, as poses.json holds it:
//   - eye: the eye's proportions in the 24 by 24 box, as half the gap between
//     the eyes, the slit's height and width, and a pupil bar's length and width.
//   - turnLimit: the turn in degrees past which a move shuts the eye.
//   - ms: how long a move between two poses takes.
//   - look: the outline and eye-edge widths, and the drift and reading
//     amplitudes, in the box's units.
//   - poses and beats.
// The studio edits a copy and writes it back, so every number the face uses
// lives here and not in styles.
export interface Table {
  eye: { dx: number; H: number; W: number; L: number; BW: number }
  turnLimit: number
  ms: number
  look: { outline: number; edge: number; drift: number; read: number }
  poses: Record<string, { forms: Form[] }>
  beats: Record<string, { steps: BeatStep[]; back: number }>
}

// TABLE is the committed table, the one the product draws with.
export const TABLE = table as Table

export const EYE = TABLE.eye
export const MOVE_MS = TABLE.ms
export const POSES = Object.keys(TABLE.poses)
export const BEATS = Object.keys(TABLE.beats)

// Beat is one beat to play. key tells two plays of the same beat apart, and
// toward, for a glance, is -1 for left and 1 for right.
export interface Beat {
  name: string
  key: number
  toward?: number
}

const NEUTRAL: Row = { slitLen: 1, slitRot: 0, eyeX: 0, eyeY: 0, pupilPos: 0, pupilScale: 1, tilt: 0, spread: 0, pupilAngle: 60, pupilRot: 0 }

const full = (r: Partial<Row>): Row => ({ ...NEUTRAL, ...r })

// mirror is the right eye of a pose that gives only the left: the sideways
// numbers flip.
const mirror = (r: Partial<Row>): Row => {
  const f = full(r)
  return { ...f, tilt: -f.tilt, eyeX: -f.eyeX, pupilRot: -f.pupilRot }
}

// formOf is the strongest form of a pose at or below intensity, so a later
// engine can send a level that this renderer does not draw yet.
function formOf(pose: string, intensity = 0, t = TABLE): Form {
  const forms = (t.poses[pose] ?? t.poses.open).forms
  return forms[Math.max(0, Math.min(Math.floor(intensity), forms.length - 1))]
}

export function rowsOf(pose: string, intensity = 0, t = TABLE): Rows {
  const f = formOf(pose, intensity, t)
  return [full(f.l), f.r ? full(f.r) : mirror(f.l)]
}

// loopOf names the pose's own loop, such as the reading motion of focused.
export const loopOf = (pose: string, intensity = 0, t = TABLE): string | undefined => formOf(pose, intensity, t).loop

// strongForms counts the forms of a pose above its base.
export const strongForms = (pose: string, t = TABLE): number => (t.poses[pose]?.forms.length ?? 1) - 1

// DRIFT_POSES are the poses whose pupil drifts, because its place says
// nothing in them.
export const DRIFT_POSES = new Set(['open', 'half-lidded', 'worried'])

// The 45-degree rule: a slit that would turn further shuts first, turns while
// it is shut, and opens into the new pose. SHUT_SHARE is the part of the move
// spent shutting.
export const SHUT_SHARE = 0.4

export const turnOf = (r: Row) => r.slitRot + r.tilt

// maxTurn is the furthest either slit turns on a move from a to b.
export const maxTurn = (a: Rows, b: Rows): number => Math.max(...a.map((r, i) => Math.abs(turnOf(b[i]) - turnOf(r))))

export function needsShut(a: Rows, b: Rows, t = TABLE): boolean {
  return maxTurn(a, b) > t.turnLimit
}

export const shut = (rows: Rows): Rows => rows.map((r) => ({ ...r, slitLen: 0, pupilScale: 0 })) as Rows

const lerp = (a: Rows, b: Rows, k: number): Rows =>
  a.map((r, i) => {
    const out = { ...r }
    for (const key of Object.keys(r) as (keyof Row)[]) out[key] = r[key] + (b[i][key] - r[key]) * k
    return out
  }) as Rows

// rowsAt is what a move from a to b shows at t in [0, 1], before easing. The
// studio's transition check samples it, and a test checks the rule with it.
export function rowsAt(a: Rows, b: Rows, at: number, t = TABLE): Rows {
  if (!needsShut(a, b, t)) return lerp(a, b, at)
  if (at < SHUT_SHARE) return lerp(a, shut(a), at / SHUT_SHARE)
  return lerp(shut(b), b, (at - SHUT_SHARE) / (1 - SHUT_SHARE))
}

// A Stage is one step of a move or a beat: the rows to ease to, how long the
// ease takes, and how long they then hold.
export interface Stage {
  rows: Rows
  ms: number
  hold: number
}

// ease is one ease from the rows before to a stage, under the 45-degree
// rule: a stage that would turn a slit too far becomes shut, turn, and open,
// in the stage's own time.
function ease(from: Rows, to: Stage, t: Table): Stage[] {
  if (!needsShut(from, to.rows, t)) return [to]
  return [
    { rows: shut(from), ms: to.ms * SHUT_SHARE, hold: 0 },
    // The turn happens while the slit is shut, so it takes no time.
    { rows: shut(to.rows), ms: 0, hold: 30 },
    { rows: to.rows, ms: to.ms * (1 - SHUT_SHARE), hold: to.hold },
  ]
}

// moveStages is the move from what shows to a new pose.
export function moveStages(from: Rows, to: Rows, t = TABLE): Stage[] {
  return ease(from, { rows: to, ms: t.ms, hold: 0 }, t)
}

// beatStages is a beat played over the rows that show, ending on the held
// pose. Each step, and the return, follows the 45-degree rule, so a beat
// that starts or ends on a turned pose such as closed does not spin. An
// unknown beat plays nothing.
export function beatStages(beat: Beat, shown: Rows, held: Rows, t = TABLE): Stage[] {
  const b = t.beats[beat.name]
  if (!b) return []
  const dir = beat.toward ?? 1
  const steps: Stage[] = b.steps.map((s) => {
    const rows = shown.map((r) => {
      let next: Row = s.row ? full(s.row) : { ...r, ...s.set }
      if (s.set?.eyeX !== undefined) next = { ...next, eyeX: s.set.eyeX * dir }
      for (const [k, v] of Object.entries(s.min ?? {}) as [keyof Row, number][]) next = { ...next, [k]: Math.max(next[k], v) }
      return next
    }) as Rows
    return { rows, ms: s.ms, hold: s.hold }
  })
  steps.push({ rows: held, ms: b.back, hold: 0 })
  const out: Stage[] = []
  let at = shown
  for (const s of steps) {
    out.push(...ease(at, s, t))
    at = s.rows
  }
  return out
}

// A Look is what a member's face shows by default: a pose, and whether the
// mark fades.
export interface Look {
  pose: string
  faded: boolean
}

const BREAKS = ['failed', 'cost', 'room']
const LIMITS = ['spend', 'turns', 'team', 'guard']

// defaultLook is the pose for a status when no engine has sent an
// expression. A paused member shows its most serious pause: a break before a
// limit, and a limit before a person.
export function defaultLook(status: Pick<TalkootMemberStatus, 'presence' | 'pauses' | 'idle'> | undefined, presence: string): Look {
  switch (presence) {
    case 'offline':
      return { pose: 'closed', faded: true }
    case 'paused': {
      const kinds = status?.pauses ?? []
      if (kinds.some((k) => BREAKS.includes(k))) return { pose: 'worried', faded: false }
      if (kinds.some((k) => LIMITS.includes(k))) return { pose: 'closed-squint', faded: false }
      return { pose: 'half-lidded', faded: false }
    }
    case 'waiting':
      return { pose: 'looking-up', faded: false }
    case 'working':
      return { pose: 'focused', faded: false }
  }
  // A worker stopped for idleness sleeps, and keeps its colour, because the
  // next envelope wakes it.
  return { pose: status?.idle ? 'closed' : 'open', faded: false }
}
