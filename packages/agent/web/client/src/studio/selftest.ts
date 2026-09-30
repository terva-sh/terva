// ?selftest plays every pose, beat, presence, team state, and the scenario,
// checks the 45-degree rule and the check's own sight, and writes the result
// to <body data-selftest>: "ok", or "fail: " and each error. A smoke test
// reads it. The browser's copy of the table is put back afterwards, so a
// self-test never keeps its rolled pose.

import { BODIES } from '../features/talkoot/MemberMark'
import { rowsAt, rowsOf } from '../features/talkoot/face/poses'
import { formId, formsOf, runCheck } from './check'
import { BEAT_BUTTONS, PRESENCES, TEAM_BODIES, TEAM_STATES } from './data'
import type { Studio } from './state'
import { insideOf } from './ui'

const KEY = 'terva-studio-table'

export function selftest(get: () => Studio) {
  const errs: string[] = []
  const kept = localStorage.getItem(KEY)
  addEventListener('error', (e) => errs.push(e.message))
  document.body.dataset.selftest = 'running'
  let at = 0
  const step = (name: string, fn: () => void) => {
    setTimeout(() => {
      try {
        fn()
      } catch (e) {
        errs.push(`${name}: ${(e as Error).message}`)
      }
    }, at)
    at += 150
  }
  const api = () => get().api
  const table = () => get().table

  for (const f of formsOf(table())) step(`pose ${formId(f)}`, () => api().showPose!(formId(f)))
  for (const b of BEAT_BUTTONS) step(`beat ${b.name}`, () => api().beat!(b.beat, b.toward))
  for (const p of PRESENCES) step(`presence ${p.name}`, () => api().presence!(p))
  for (const s of Object.keys(TEAM_STATES)) step(`team state ${s}`, () => api().teamState!(s))
  for (const b of Object.keys(TEAM_BODIES)) step(`team body ${b}`, () => api().teamBody!(b))
  for (const n of [16, 32, 48, 24]) step(`grid ${n}`, () => api().gridSize!(n))
  step('scenario', () => api().scenario!())

  // The rule: closed to open turns 90 degrees, so the move shuts on its way.
  // open to focused does not turn, so it stays open.
  step('the 45-degree rule', () => {
    const t = table()
    if (rowsAt(rowsOf('closed', 0, t), rowsOf('open', 0, t), 0.4, t)[0].slitLen > 0.01) errs.push('closed to open did not shut to turn')
    if (rowsAt(rowsOf('open', 0, t), rowsOf('focused', 0, t), 0.4, t)[0].slitLen < 0.5) errs.push('open to focused shut without a turn')
  })
  // A positive control for the check: with the rule off, closed to open spins,
  // and the check has to see it.
  step('the check sees a spin', () => {
    const off = { ...table(), turnLimit: 180 }
    // A spin does not depend on the body, so one body is enough.
    const one = { circle: BODIES.circle }
    if (!runCheck(off, one, insideOf(one)).some((x) => x.kind === 'spin' && x.from === 'closed' && x.to === 'open'))
      errs.push('the check missed the closed to open spin with the rule off')
  })
  step('the check runs', () => {
    const f = api().runCheck!()
    if (!api().strips!()) errs.push('no filmstrips')
    document.body.dataset.findings = String(f.length)
  })
  step('roll', () => {
    if (!api().roll!()) errs.push('roll made no pose')
  })
  // The drift and the reading scan are attached where they belong, and not
  // elsewhere.
  step('loops', () => {
    const anim = (pose: string, part: string, faded = false) => {
      const svg = [...document.querySelectorAll(`svg.talkoot-mark.motion-full[data-pose="${pose}"]`)].find((s) => s.classList.contains('faded') === faded)
      const el = svg?.querySelector(part)
      return el ? getComputedStyle(el).animationName : `no ${pose} mark`
    }
    if (anim('open', '.wanderer') !== 'talkoot-wander') errs.push(`no drift on an open face: ${anim('open', '.wanderer')}`)
    if (anim('looking-up', '.wanderer') === 'talkoot-wander') errs.push('drift on a looking-up face')
    if (anim('focused', '.eyes:not(.edge)') !== 'talkoot-read') errs.push(`no reading scan on a focused face: ${anim('focused', '.eyes:not(.edge)')}`)
  })
  step('eye shape', () => {
    const h = table().eye.H
    get().edit((t) => void (t.eye.H = 6))
    setTimeout(() => get().edit((t) => void (t.eye.H = h)), 50)
  })

  // The scenario is the longest step. It runs at the page's speed.
  const scenarioMs = 22_400 / get().settings.speed
  setTimeout(() => {
    if (kept === null) localStorage.removeItem(KEY)
    else localStorage.setItem(KEY, kept)
    document.body.dataset.selftest = errs.length ? 'fail: ' + errs.join(' | ') : 'ok'
  }, at + scenarioMs + 1000)
}
