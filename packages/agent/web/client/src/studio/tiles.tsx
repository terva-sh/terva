// The animation tiles: each repeats one animation on its own, so nothing needs
// a click.

import { useEffect, useState } from 'preact/hooks'
import { BODIES, FaceMark, type Body, type Face } from '../features/talkoot/MemberMark'
import { parseForm } from './check'
import { TEAM_BODIES } from './data'
import { useStudio } from './state'
import { Section } from './ui'

// A Mark is what a tile's script can change on its face.
interface Mark {
  pose: (id: string) => void
  beat: (name: string, toward?: number) => void
  fade: (f: boolean) => void
  size: (px: number) => void
}

type Step = [number, string?, ((m: Mark, pupilMin: number) => void)?]

interface Tile {
  title: string
  note: string
  body: string
  team?: boolean
  color: string
  size?: number
  steps: Step[]
}

const TILES: Tile[] = [
  { title: 'Idle', note: 'The breath and the plain blink. Ambient, so they mean nothing.', body: 'circle', color: '#46A758', steps: [[0, 'open, ambient motion', (m) => m.pose('open')], [6000]] },
  {
    title: 'Idle drift',
    note: 'Ambient: the pupil wanders a little along its slit. It never moves sideways, and never as far as looking up.',
    body: 'pill',
    color: '#12A594',
    steps: [[0, 'open, pupil drifting', (m) => m.pose('open')], [13000]],
  },
  { title: 'Working', note: 'focused: the faster bob, and eyes that hop along as if reading.', body: 'hexagon', color: '#3E63DD', steps: [[0, 'focused', (m) => m.pose('focused')], [5000]] },
  { title: 'Waiting on you', note: 'looking-up, the gaze moves from side to side.', body: 'shield', color: '#FFB224', steps: [[0, 'looking-up', (m) => m.pose('looking-up')], [6000]] },
  {
    title: 'Skeptical',
    note: 'Flat slits at eye height that stretch and shrink in turn.',
    body: 'blob',
    color: '#6E56CF',
    steps: [[0, 'skeptical, entered through a shut eye', (m) => m.pose('skeptical')], [3500, 'back to open', (m) => m.pose('open')], [1800]],
  },
  { title: 'Happy', note: 'A beat: the pupil bars open into arcs.', body: 'tab', color: '#12A594', steps: [[0, 'open', (m) => m.pose('open')], [900, 'happy', (m) => m.beat('happy')], [2600]] },
  {
    title: 'Glance',
    note: 'A beat toward the teammate it messaged.',
    body: 'drop',
    color: '#D6409F',
    steps: [[0, 'focused', (m) => m.pose('focused')], [900, 'glance right', (m) => m.beat('glance', 1)], [1600, 'glance left', (m) => m.beat('glance', -1)], [1800]],
  },
  { title: 'Slow blink', note: 'A beat: a person answered, or approved.', body: 'rounded-square', color: '#F76B15', steps: [[0, 'open', (m) => m.pose('open')], [900, 'slow-blink', (m) => m.beat('slow-blink')], [2400]] },
  { title: 'Fast blink', note: 'A beat: a message arrived while it was idle.', body: 'pill', color: '#00A2C7', steps: [[0, 'open', (m) => m.pose('open')], [900, 'fast-blink', (m) => m.beat('fast-blink')], [1800]] },
  {
    title: 'Are you still there?',
    note: 'Intensity: a question that has waited past a threshold raises the brow.',
    body: 'shield',
    color: '#FFB224',
    steps: [
      [0, 'looking-up: ask card 42 opened', (m) => m.pose('looking-up')],
      [3200, 'looking-up, strong: card 42 open 15 min', (m) => m.pose('looking-up, strong')],
      [
        3200,
        'slow-blink: a person answered',
        (m) => {
          m.pose('open')
          m.beat('slow-blink')
        },
      ],
      [2200],
    ],
  },
  {
    title: 'Getting worse',
    note: 'Intensity: a pose gains its brows when the signal repeats or grows.',
    body: 'rounded-square',
    color: '#E5484D',
    steps: [
      [0, 'focused', (m) => m.pose('focused')],
      [1400, 'frustrated: tool_error x3', (m) => m.pose('frustrated')],
      [2000, 'frustrated, strong: tool_error x6', (m) => m.pose('frustrated, strong')],
      [2200, 'worried: provider retry', (m) => m.pose('worried')],
      [1800, 'worried, strong: retry 3 of 4', (m) => m.pose('worried, strong')],
      [2200, 'focused: retry ok', (m) => m.pose('focused')],
      [1600],
    ],
  },
  {
    title: 'Things go wrong',
    note: 'Straight transitions: focused, frustrated, worried.',
    body: 'triangle',
    color: '#E5484D',
    steps: [
      [0, 'focused', (m) => m.pose('focused')],
      [1600, 'frustrated: tool_error x3', (m) => m.pose('frustrated')],
      [2000, 'worried: provider retry', (m) => m.pose('worried')],
      [2000, 'focused: retry ok', (m) => m.pose('focused')],
      [1600],
    ],
  },
  {
    title: 'A lid comes down',
    note: 'The slit would turn 90 degrees, past the limit, so the eye shuts, turns, and opens flat. Leaving closed does the same.',
    body: 'hexagon',
    color: '#AB4ABA',
    steps: [[0, 'focused', (m) => m.pose('focused')], [1400, 'closed', (m) => m.pose('closed')], [2000, 'open', (m) => m.pose('open')], [1400]],
  },
  {
    title: 'Paused, and pauses stacking',
    note: 'A person pauses it, then its spend cap trips. The limit wins.',
    body: 'cloud',
    color: '#A18072',
    steps: [
      [0, 'working', (m) => m.pose('focused')],
      [1400, 'paused [person]: half-lidded', (m) => m.pose('half-lidded')],
      [2000, 'paused [person, spend]: closed-squint', (m) => m.pose('closed-squint')],
      [2200, 'resumed', (m) => m.pose('open')],
      [1400],
    ],
  },
  {
    title: 'Asleep, and waking',
    note: 'A worker stopped for idleness keeps its colour. An envelope wakes it.',
    body: 'circle',
    color: '#5B8C3A',
    steps: [
      [
        0,
        'closed: idle stopped',
        (m) => {
          m.fade(false)
          m.pose('closed')
        },
      ],
      [2400, 'envelope: wakes', (m) => m.pose('open')],
      [700, 'fast-blink', (m) => m.beat('fast-blink')],
      [1400, 'focused', (m) => m.pose('focused')],
      [1800],
    ],
  },
  {
    title: 'Going offline',
    note: 'Nothing listens for it, so it closes and fades.',
    body: 'shield',
    color: '#8D8D8D',
    steps: [
      [
        0,
        'open',
        (m) => {
          m.fade(false)
          m.pose('open')
        },
      ],
      [
        1400,
        'offline: closed, faded',
        (m) => {
          m.pose('closed')
          m.fade(true)
        },
      ],
      [2400],
    ],
  },
  {
    title: 'The pupil pops',
    note: 'The mark crosses the size cutoff, and the pupil pops out and back.',
    body: 'blob',
    color: '#3E63DD',
    size: 28,
    steps: [[0, 'above the cutoff', (m, min) => m.size(Math.max(min + 4, 28))], [1600, 'below the cutoff', (m, min) => m.size(Math.max(min - 4, 8))], [1600]],
  },
  {
    title: 'The team mark',
    note: 'Combined presence: online, busy, needs you, paused, offline.',
    body: 'trio',
    team: true,
    color: '#F29100',
    steps: [
      [
        0,
        'online',
        (m) => {
          m.fade(false)
          m.pose('open')
        },
      ],
      [1600, 'busy', (m) => m.pose('focused')],
      [1800, 'needs you', (m) => m.pose('looking-up')],
      [2400, 'paused', (m) => m.pose('half-lidded')],
      [
        1800,
        'offline',
        (m) => {
          m.pose('closed')
          m.fade(true)
        },
      ],
      [2000],
    ],
  },
]

let beats = 0

function TileView({ tile }: { tile: Tile }) {
  const { table, settings, later } = useStudio()
  const [face, setFace] = useState<Face>({ pose: 'open' })
  const [size, setSize] = useState(tile.size ?? 96)
  const [now, setNow] = useState('')
  const body: Body = (tile.team ? TEAM_BODIES : BODIES)[tile.body]
  useEffect(() => {
    let live = true
    const timers: number[] = []
    const mark: Mark = {
      pose: (id) => {
        const f = parseForm(id, table)
        if (f) setFace((x) => ({ ...x, pose: f.pose, intensity: f.intensity }))
      },
      beat: (name, toward) => setFace((x) => ({ ...x, beat: { name, key: ++beats, toward } })),
      fade: (faded) => setFace((x) => ({ ...x, faded })),
      size: setSize,
    }
    const run = () => {
      // Each pass's timers have all fired by the time the next pass starts.
      timers.length = 0
      let at = 0
      for (const [delay, label, act] of tile.steps) {
        at += delay
        if (act)
          timers.push(
            later(() => {
              if (!live) return
              setNow(label ?? '')
              act(mark, settings.pupilMin)
            }, at),
          )
      }
      timers.push(later(() => live && run(), at + 1))
    }
    run()
    return () => {
      live = false
      for (const t of timers) clearTimeout(t)
    }
    // A new speed or cutoff restarts the script at its own pace.
  }, [settings.speed, settings.pupilMin])
  return (
    <div class="tile">
      <div class="pair">
        <FaceMark body={body} shape={tile.body} color={tile.color} size={size} face={face} />
      </div>
      <div class="now">{now}</div>
      <b>{tile.title}</b>
      <span>{tile.note}</span>
    </div>
  )
}

export function Tiles() {
  return (
    <Section
      id="animations"
      title="Animations"
      note="Each tile repeats one animation on its own, so nothing needs a click. The orange line names the step that is playing. Use Speed in the header to slow them down, and Motion to see what subtle and off keep."
    >
      <div class="tiles">
        {TILES.map((t) => (
          <TileView key={t.title} tile={t} />
        ))}
      </div>
    </Section>
  )
}
