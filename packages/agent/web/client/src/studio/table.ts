// The face's pose table as a file: the shape the renderer reads (validTable)
// and the text poses.json is committed in (formatTable). The studio checks a
// table with these before it sends one, and the dev server's Save
// (scripts/studio-save.ts) checks it again before it writes.

import type { Table } from '../features/talkoot/face/poses'

export const ROW_KEYS = ['slitLen', 'slitRot', 'eyeX', 'eyeY', 'pupilPos', 'pupilScale', 'tilt', 'spread', 'pupilAngle', 'pupilRot']
const LOOPS = ['focus', 'lookup', 'skeptic']
// NAME is a pose or beat name: lower-case letters, digits, and hyphens.
export const NAME = /^[a-z][a-z0-9-]{0,39}$/

type Obj = Record<string, unknown>

const isObj = (v: unknown): v is Obj => typeof v === 'object' && v !== null && !Array.isArray(v)
const num = (v: unknown): v is number => typeof v === 'number' && Number.isFinite(v)

function onlyKeys(v: unknown, keys: string[], where: string): string | null {
  if (!isObj(v)) return `${where} is not an object`
  for (const k of Object.keys(v)) if (!keys.includes(k)) return `${where} has an unknown key ${JSON.stringify(k)}`
  return null
}

// numbers checks that v holds only the given keys, each a number. required
// asks for every key, and a partial row asks only for the keys it has.
function numbers(v: unknown, keys: string[], where: string, required: boolean): string | null {
  const bad = onlyKeys(v, keys, where)
  if (bad) return bad
  const o = v as Obj
  for (const k of required ? keys : Object.keys(o)) if (!num(o[k])) return `${where}.${k} is not a number`
  return null
}

function validForm(f: unknown, where: string): string | null {
  const keys = onlyKeys(f, ['l', 'r', 'loop'], where)
  if (keys) return keys
  const o = f as Obj
  const l = numbers(o.l, ROW_KEYS, `${where}.l`, true)
  if (l) return l
  if (o.r !== null) {
    const r = numbers(o.r, ROW_KEYS, `${where}.r`, true)
    if (r) return r
  }
  if (o.loop !== undefined && !LOOPS.includes(o.loop as string)) return `${where}.loop is not one of ${LOOPS.join(', ')}`
  return null
}

function validStep(s: unknown, where: string): string | null {
  const keys = onlyKeys(s, ['row', 'set', 'min', 'ms', 'hold'], where)
  if (keys) return keys
  const o = s as Obj
  for (const part of ['row', 'set', 'min']) {
    if (o[part] === undefined) continue
    const bad = numbers(o[part], ROW_KEYS, `${where}.${part}`, false)
    if (bad) return bad
  }
  if (!num(o.ms) || o.ms < 0 || !num(o.hold) || o.hold < 0) return `${where} has no ms and hold`
  return null
}

// validTable returns why t is not a pose table the renderer can read, or null.
export function validTable(t: unknown): string | null {
  const top = onlyKeys(t, ['eye', 'turnLimit', 'ms', 'look', 'poses', 'beats'], 'the table')
  if (top) return top
  const o = t as Obj
  const eye = numbers(o.eye, ['dx', 'H', 'W', 'L', 'BW'], 'eye', true)
  if (eye) return eye
  if (!num(o.turnLimit) || o.turnLimit < 0 || o.turnLimit > 180) return 'turnLimit is not a number from 0 to 180'
  if (!num(o.ms) || o.ms <= 0 || o.ms > 10000) return 'ms is not a number from 1 to 10000'
  const look = numbers(o.look, ['outline', 'edge', 'drift', 'read'], 'look', true)
  if (look) return look
  if (!isObj(o.poses) || !isObj(o.poses.open)) return 'poses has no open pose'
  for (const [name, pose] of Object.entries(o.poses)) {
    if (!NAME.test(name)) return `the pose name ${JSON.stringify(name)} is not lower-case letters, digits, and hyphens`
    const where = `poses.${name}`
    const keys = onlyKeys(pose, ['forms'], where)
    if (keys) return keys
    const forms = (pose as Obj).forms
    if (!Array.isArray(forms) || forms.length < 1 || forms.length > 4) return `${where}.forms is not a list of one to four forms`
    for (const [i, f] of forms.entries()) {
      const bad = validForm(f, `${where}.forms[${i}]`)
      if (bad) return bad
    }
  }
  if (!isObj(o.beats)) return 'beats is not an object'
  for (const [name, beat] of Object.entries(o.beats)) {
    if (!NAME.test(name)) return `the beat name ${JSON.stringify(name)} is not lower-case letters, digits, and hyphens`
    const where = `beats.${name}`
    const keys = onlyKeys(beat, ['steps', 'back'], where)
    if (keys) return keys
    const b = beat as Obj
    if (!num(b.back) || b.back < 0) return `${where}.back is not a number`
    if (!Array.isArray(b.steps) || b.steps.length < 1) return `${where}.steps is empty`
    for (const [i, s] of b.steps.entries()) {
      const bad = validStep(s, `${where}.steps[${i}]`)
      if (bad) return bad
    }
  }
  return null
}

// formatTable is the file's text: one-space indent and a final newline, the
// form poses.json is committed in, so a Save with no edit changes nothing.
export function formatTable(t: Table): string {
  const ordered = { eye: t.eye, turnLimit: t.turnLimit, ms: t.ms, look: t.look, poses: t.poses, beats: t.beats }
  return JSON.stringify(ordered, null, 1) + '\n'
}
