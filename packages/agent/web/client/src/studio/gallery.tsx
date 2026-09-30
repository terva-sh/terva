// The views that show many marks at once: every body in every pose, a sidebar
// of eight members, and the talkoot's own mark beside member marks.

import { useState } from 'preact/hooks'
import { BODIES, FaceMark } from '../features/talkoot/MemberMark'
import { defaultLook } from '../features/talkoot/face/poses'
import { formId, formsOf } from './check'
import { NAMES, PALETTE, PRESENCES, SHAPES, TEAM_BODIES, TEAM_STATES } from './data'
import { useStudio } from './state'
import { Buttons, Row, Section, Swatches } from './ui'

const sizeOf = (v: string | null, fallback: number) => (v && Number(v) > 0 ? Number(v) : fallback)

export function Grid() {
  const { table, q, api } = useStudio()
  const [size, setSize] = useState(() => sizeOf(q.get('grid'), 24))
  api.gridSize = setSize
  const forms = formsOf(table)
  const bodies = Object.keys(BODIES)
  return (
    <Section
      id="poses"
      title="Every body in every held pose"
      note="Check that each pose reads on each body, that narrow bodies do not clip the eyes, and that closed and skeptical stay apart at small sizes. Below the pupil cutoff in the header the pupil does not draw."
    >
      <Row k="Size">
        <Buttons names={['16', '24', '32', '48']} on={(n) => Number(n) === size} pick={(n) => setSize(Number(n))} />
      </Row>
      <table class="grid" style={{ '--cell': `${Math.max(size, 54)}px` }}>
        <thead>
          <tr>
            <th />
            {bodies.map((b) => (
              <th key={b} class="b">
                {b.replace('-', '-​')}
              </th>
            ))}
          </tr>
        </thead>
        <tbody>
          {forms.map((f, i) => (
            <tr key={formId(f)}>
              <th class="r">{formId(f)}</th>
              {bodies.map((b, j) => (
                <td key={b}>
                  <FaceMark body={BODIES[b]} shape={b} color={PALETTE[(i + j * 3) % PALETTE.length]} size={size} face={{ pose: f.pose, intensity: f.intensity }} />
                </td>
              ))}
            </tr>
          ))}
        </tbody>
      </table>
    </Section>
  )
}

export function Sidebar() {
  const { q } = useStudio()
  const [size, setSize] = useState(() => sizeOf(q.get('side'), 20))
  return (
    <Section
      id="sidebar"
      title="A sidebar of eight members"
      note="Every presence at once, at list size, with the state label beside each mark. The faces come from the product's defaultLook, so this is what the Talkoot sidebar draws. Check that the face adds to the label and that each state reads at a glance."
    >
      <Row k="Size">
        <Buttons names={['16', '20', '24', '32']} on={(n) => Number(n) === size} pick={(n) => setSize(Number(n))} />
      </Row>
      <div class="sidebar">
        {PRESENCES.map((p, i) => {
          const look = defaultLook(p.status, p.presence)
          return (
            <div class="member" key={p.name}>
              <FaceMark body={BODIES[SHAPES[i]]} shape={SHAPES[i]} color={PALETTE[(i * 5 + 2) % PALETTE.length]} size={size} face={look} />
              <div>
                <div class="name">{NAMES[i]}</div>
                <div class="state">{p.label}</div>
              </div>
            </div>
          )
        })}
      </div>
    </Section>
  )
}

export function Team() {
  const { q, api } = useStudio()
  const [body, setBody] = useState(() => (q.get('team') && TEAM_BODIES[q.get('team')!] ? q.get('team')! : 'trio'))
  const [color, setColor] = useState('#F29100')
  const [state, setState] = useState('online')
  api.teamState = setState
  api.teamBody = setBody
  const look = TEAM_STATES[state]
  const lines: [string | null, string, number][] = [
    [null, 'Team: payments rework', 0],
    ['Helm', 'Jev, take the ledger migration. Atlas reviews.', 0],
    ['Jev', 'On it. Branch feat/ledger-migration.', 1],
    ['Atlas', 'Waiting for the diff.', 2],
  ]
  return (
    <Section
      id="team"
      title="The talkoot's own mark"
      note="Candidate team-only bodies. None of them is one of the member shapes. The room log below puts the team mark beside member marks at 16 pixels, to check that nobody mistakes one for the other."
    >
      <div class="play two">
        <div class="stage" id="teamstage">
          <FaceMark body={TEAM_BODIES[body]} shape={body} color={color} size={160} face={look} />
        </div>
        <div>
          <Row k="Body">
            <Buttons names={Object.keys(TEAM_BODIES)} on={(n) => n === body} pick={setBody} />
          </Row>
          <Row k="Colour">
            <Swatches pick={setColor} />
          </Row>
          <Row k="Team state">
            <Buttons names={Object.keys(TEAM_STATES)} on={(n) => n === state} pick={setState} />
          </Row>
          <div class="room" id="room">
            {lines.map(([who, text, i]) => (
              <div class="line" key={text}>
                {who === null ? (
                  <FaceMark body={TEAM_BODIES[body]} shape={body} color={color} size={16} face={look} />
                ) : (
                  <FaceMark body={BODIES[SHAPES[i]]} shape={SHAPES[i]} color={PALETTE[(i * 5 + 2) % PALETTE.length]} size={16} face={{ pose: 'open' }} />
                )}
                <span class="who">{who ?? text}</span>
                <span>{who ? text : ''}</span>
              </div>
            ))}
          </div>
        </div>
      </div>
    </Section>
  )
}
