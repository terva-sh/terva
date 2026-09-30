// Transitions: a filmstrip of every move from one pose, the transition check,
// and the gallery of every clip the check finds.

import { useEffect, useMemo, useState } from 'preact/hooks'
import { BODIES, FaceMark, type Face } from '../features/talkoot/MemberMark'
import { needsShut, maxTurn, type Table } from '../features/talkoot/face/poses'
import { TuningContext, useTuning } from '../features/talkoot/face/tuning'
import agreed from './agreed-clips.json'
import { clipKey, formId, formsOf, frameAt, glanceRows, parseForm, rowsOfForm, runCheck, summary, type Finding } from './check'
import { PALETTE } from './data'
import { useStudio } from './state'
import { insideOf, Row, Section } from './ui'

const FRAMES = [0, 1 / 6, 2 / 6, 3 / 6, 4 / 6, 5 / 6, 1]
const AGREED = new Set<string>(agreed)

// check runs the transition check on every member body with the live table.
export const useCheck = () => {
  const { table } = useStudio()
  return () => runCheck(table, BODIES, insideOf(BODIES))
}

export function Transitions() {
  const { table, later, api } = useStudio()
  const tuning = useTuning()
  const check = useCheck()
  const ids = formsOf(table).map(formId)
  const [from, setFrom] = useState('closed')
  const [body, setBody] = useState('hexagon')
  const [findings, setFindings] = useState<Finding[] | null>(null)
  const [checked, setChecked] = useState<Table | null>(null)
  const [highlight, setHighlight] = useState('')
  const [face, setFace] = useState<Face>({ pose: 'closed' })
  const [jump, setJump] = useState(0)

  const fromRef = parseForm(from, table) ?? { pose: 'open', intensity: 0 }
  const a = rowsOfForm(fromRef, table)
  const showFrom = () => {
    setFace({ pose: fromRef.pose, intensity: fromRef.intensity })
    setJump((j) => j + 1)
  }
  useEffect(showFrom, [from, body])
  const play = (to: string) => {
    showFrom()
    const t = parseForm(to, table)
    if (t) later(() => setFace({ pose: t.pose, intensity: t.intensity }), 300)
  }
  // The check takes seconds, so an edit does not rerun it. It marks the
  // findings stale instead, and a click runs it again.
  const run = () => {
    const f = check()
    setFindings(f)
    setChecked(table)
    return f
  }
  const stale = !!findings && checked !== table
  api.runCheck = run
  api.strips = () => ids.length - 1

  const flagged = (to: string) => (findings ?? []).filter((x) => x.from === from && x.to === to && (x.body === body || x.kind !== 'clip'))
  // The large mark plays at a quarter speed, so a move can be watched.
  const slow = useMemo(() => ({ ...tuning, speed: tuning.speed * 0.25 }), [tuning])

  return (
    <Section
      id="transitions"
      title="Transitions"
      note='Pick a pose to start from. Each strip shows seven still frames of the move to another pose, so a spin, a vanish, or a clip shows without playing anything. Click a strip to play it at a quarter speed on the large mark. A move that turns the slit further than "Shut to turn past" shuts the eye, turns it while it is shut, and opens it into the new pose.'
    >
      <div class="play two">
        <div>
          <div class="stage" id="tstage">
            <TuningContext.Provider value={slow}>
              <FaceMark key={jump} body={BODIES[body]} shape={body} color="#3E63DD" size={160} face={face} />
            </TuningContext.Provider>
          </div>
          <Row k="From">
            <select id="tfrom" value={from} onChange={(e) => setFrom((e.target as HTMLSelectElement).value)}>
              {ids.map((n) => (
                <option key={n}>{n}</option>
              ))}
            </select>
          </Row>
          <Row k="Body">
            <select id="tbody" value={body} onChange={(e) => setBody((e.target as HTMLSelectElement).value)}>
              {Object.keys(BODIES).map((n) => (
                <option key={n}>{n}</option>
              ))}
            </select>
          </Row>
          <Row k="Check">
            <button id="check" onClick={run}>
              Run the transition check
            </button>
          </Row>
          <div class="readout" id="tsummary">
            {findings ? (stale ? 'The table changed since this check. Run it again.\n\n' : '') + summary(table, findings) : ''}
          </div>
        </div>
        <div>
          <div class="strips" id="strips">
            {ids
              .filter((to) => to !== from)
              .map((to) => {
                const toRef = parseForm(to, table)!
                const b = rowsOfForm(toRef, table)
                const why = flagged(to)
                return (
                  <div
                    key={to}
                    class={why.length ? 'strip flagged' : 'strip'}
                    onClick={() => play(to)}
                    ref={(el) => {
                      if (el && to === highlight) el.scrollIntoView({ block: 'center' })
                    }}
                  >
                    <span class="to">
                      {from} → <b>{to}</b>
                    </span>
                    {FRAMES.map((t) => (
                      <span key={t}>
                        <FaceMark body={BODIES[body]} color="#3E63DD" size={40} frame={frameAt(a, b, t, table)} />
                      </span>
                    ))}
                    {why.length ? (
                      <span class="why">{[...new Set(why.map((x) => x.kind))].join(', ')}</span>
                    ) : (
                      <span class="rule">{needsShut(a, b, table) ? `shuts to turn ${Math.round(maxTurn(a, b))}°` : ''}</span>
                    )}
                  </div>
                )
              })}
          </div>
          <h2 style={{ marginTop: '14px' }}>What the check found</h2>
          <p>
            Every pair of poses on every body, sampled through the move, and each held pose with a glance either way. <b>spin</b>: a visible slit turns more
            than 30 degrees. <b>vanish</b>: both eyes nearly disappear mid-move without a shut. <b>clip</b>: part of an eye, counting its width, leaves the body. <b>touch</b>: the two eyes
            nearly meet. A clip on the agreed list (agreed-clips.json) is marked, and the CI gate fails on any other finding. Click a finding to open its strip.
          </p>
          <div class="findings" id="findings" data-findings={findings ? JSON.stringify(findings) : undefined}>
            {!findings ? 'Not run yet.' : !findings.length ? 'Nothing found.' : null}
            {(findings ?? []).map((x, i) => (
              <div
                key={i}
                onClick={() => {
                  setFrom(x.from)
                  if (x.body) setBody(x.body)
                  setHighlight(x.to)
                }}
              >
                {`${x.kind.padEnd(7)} ${x.to ? x.from + ' → ' + x.to : x.from + ', held'}${x.body ? '  on ' + x.body : ''}`}
                {x.kind === 'clip' && AGREED.has(clipKey(x)) ? '  (agreed)' : ''}
              </div>
            ))}
          </div>
        </div>
      </div>
    </Section>
  )
}

export function Clips() {
  const { table, settings, q } = useStudio()
  const check = useCheck()
  const [found, setFound] = useState<Finding[] | null>(null)
  const [checked, setChecked] = useState<Table | null>(null)
  const draw = () => {
    const clips = check()
      .filter((x) => x.kind === 'clip' && (!q.get('clipBody') || x.body === q.get('clipBody')))
      .slice(0, Number(q.get('clipMax') ?? 999))
    setFound(clips)
    setChecked(table)
  }
  useEffect(() => {
    if (q.get('only') === 'clips') draw()
  }, [])
  const stale = !!found && checked !== table
  const size = Number(q.get('clipSize') ?? 96)
  const bodies = Object.keys(BODIES)
  return (
    <Section
      id="clips"
      title="Clips"
      note='Each clip the transition check finds, drawn large at the moment it clips: a held pose, or a pose at the far end of a glance. Try "Eye edge" in the header, and the gallery redraws. The count comes from the check, so it shows what a fix removes.'
    >
      <Row k="Clips">
        <button onClick={draw}>Run the check and draw the clips</button>
        <span class="readout">
          {found ? `${found.length} clips` + (settings.edge !== 'none' ? `, eye edge: ${settings.edge}` : '') + (stale ? '. The table changed: run the check again.' : '') : ''}
        </span>
      </Row>
      <div class="tiles">
        {(found ?? []).map((x, i) => {
          const f = parseForm(x.from, table)!
          const glance = x.to.startsWith('glance') ? (x.to.endsWith('left') ? -1 : 1) : 0
          const held = rowsOfForm(f, table)
          const rows = glance ? glanceRows(held, glance, table) : held
          return (
            <div class="tile" key={i}>
              <div class="pair">
                <FaceMark body={BODIES[x.body]} shape={x.body} color={PALETTE[(bodies.indexOf(x.body) * 3) % PALETTE.length]} size={size} frame={rows} />
              </div>
              <b>
                {x.from}
                {x.to ? ', ' + x.to : ''}
              </b>
              <span>
                {x.body}
                {AGREED.has(clipKey(x)) ? ', agreed' : ''}
              </span>
            </div>
          )
        })}
      </div>
    </Section>
  )
}
