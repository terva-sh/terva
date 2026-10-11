import { createContext } from 'preact'
import { useContext, useEffect, useLayoutEffect, useRef, useState } from 'preact/hooks'
import type { TalkootMark } from '../../platform/ctrlproto/types'
import { eyeColor, markOf } from '../../platform/talkoot/marks'
import { useMotionState, type Motion } from './face/motion'
import { beatStages, DRIFT_POSES, loopOf, moveStages, rowsOf, type Beat, type Rows, type Stage, type Table } from './face/poses'
import { DEFAULT_TUNING, useTuning, type Edge } from './face/tuning'
import './face/face.css'

// MarksContext holds each member's mark by id. The team view provides it, so
// a room line or an inbox card draws a member's mark without the marks passing
// through every component between.
export const MarksContext = createContext<Record<string, TalkootMark>>({})

// A Body is a shape in a 24 by 24 box, the height its eyes sit at, and, for a
// body that is not symmetric, the x its eyes centre on.
export interface Body {
  el: preact.JSX.Element
  eyes: number
  cx?: number
}

// BODIES are the member shapes.
export const BODIES: Record<string, Body> = {
  circle: { el: <circle cx="12" cy="12" r="10" />, eyes: 11 },
  blob: { el: <path d="M12 2c5.5 0 10 3.6 10 9.3S17.6 22 11.6 22 2 18.3 2 13.1 6.5 2 12 2z" />, eyes: 11 },
  'rounded-square': { el: <rect x="2" y="2" width="20" height="20" rx="6" />, eyes: 11 },
  pill: { el: <rect x="1" y="6" width="22" height="12" rx="6" />, eyes: 12 },
  triangle: { el: <path d="M12 2.5 22 21H2z" />, eyes: 15 },
  hexagon: { el: <path d="M12 1.5 21.5 7v10L12 22.5 2.5 17V7z" />, eyes: 11 },
  cloud: { el: <path d="M7 20a5 5 0 0 1-.6-10A6.5 6.5 0 0 1 19 9.2 5.4 5.4 0 0 1 18 20z" />, eyes: 14 },
  drop: { el: <path d="M12 2s-8 9.2-8 13.6a8 8 0 0 0 16 0C20 11.2 12 2 12 2z" />, eyes: 15 },
  tab: { el: <path d="M3 7a3 3 0 0 1 3-3h5l3 3h4a3 3 0 0 1 3 3v8a3 3 0 0 1-3 3H6a3 3 0 0 1-3-3z" />, eyes: 13 },
  shield: { el: <path d="M12 2 20 5v6c0 5.5-3.5 9.5-8 11-4.5-1.5-8-5.5-8-11V5z" />, eyes: 11 },
  gem: { el: <path d="M7 2.5h10a1.5 1.5 0 0 1 1.2.6L22.5 11.5 12 22.5 1.5 11.5 5.8 3.1A1.5 1.5 0 0 1 7 2.5z" />, eyes: 9.2 },
}

// TEAM_BODY is the team mark's body, the trio: three puffs of different
// sizes, set off the axis around the largest, which carries the eyes. No
// member shape looks like it, so a team never reads as one of its members.
// docs/proposals/talkoot-members.md, "The talkoot's own mark", chose it.
export const TEAM_BODY: Body = {
  el: (
    <>
      <circle cx="9.5" cy="13.5" r="7.5" />
      <circle cx="16.8" cy="8.2" r="4.9" />
      <circle cx="18.3" cy="16.8" r="4.2" />
    </>
  ),
  eyes: 13.2,
  cx: 9.8,
}

// drawnShapes lists the shapes MemberMark draws, for a test that every shape
// in the set has a body of its own.
export const drawnShapes = (): string[] => Object.keys(BODIES)

// PUPIL_MIN is the smallest size, in pixels, that draws the pupil.
export const PUPIL_MIN = DEFAULT_TUNING.pupilMin

// A Face is what a live mark shows: a held pose at an intensity, a beat to
// play once, and whether the mark fades. A mark without one draws open eyes
// and stays still.
export interface Face {
  pose: string
  intensity?: number
  beat?: Beat
  faded?: boolean
}

const STILL: Face = { pose: 'open' }

// MemberMark draws a member's mark: its shape in its colour, an outline that
// contrasts with the theme, and the face.
export function MemberMark({
  mark,
  size = 16,
  label,
  face,
}: {
  mark: TalkootMark | undefined
  size?: number
  label?: string
  face?: Face
}) {
  if (!mark?.shape || !mark.color) return null
  return <FaceMark body={BODIES[mark.shape] ?? BODIES.circle} shape={mark.shape} color={mark.color} size={size} label={label} face={face} />
}

let masks = 0

// FaceMark draws any body with the face. The studio calls it for bodies that
// are not member shapes, and with a frame: fixed rows, drawn still, for a
// filmstrip or a clip.
export function FaceMark({
  body,
  shape,
  color,
  size = 16,
  label,
  face,
  frame,
}: {
  body: Body
  shape?: string
  color: string
  size?: number
  label?: string
  face?: Face
  frame?: Rows
}) {
  const tuning = useTuning()
  const { setting, reduced } = useMotionState()
  const live = !!face && !frame
  const motion: Motion = !live || reduced ? 'off' : (tuning.motion ?? setting)
  const f = face ?? STILL
  // Below the pupil's size a strong form has no brow to draw, so it draws as
  // its base pose.
  const tiny = size < tuning.pupilMin
  const intensity = tiny ? 0 : (f.intensity ?? 0)
  const loop = loopOf(f.pose, intensity, tuning.table)
  const cls = [
    'talkoot-mark',
    live ? `motion-${motion}` : 'still',
    tiny ? 'tiny' : '',
    f.faded ? 'faded' : '',
    loop ? `loop-${loop}` : '',
    DRIFT_POSES.has(f.pose) ? 'wander' : '',
    tuning.drift ? '' : 'no-drift',
    tuning.read ? '' : 'no-read',
  ]
    .filter(Boolean)
    .join(' ')
  return (
    <svg
      class={cls}
      width={size}
      height={size}
      viewBox="0 0 24 24"
      role={label ? 'img' : undefined}
      aria-label={label}
      aria-hidden={label ? undefined : true}
      data-shape={shape}
      data-pose={f.pose}
      data-intensity={intensity || undefined}
    >
      <Eyes body={body} color={color} pose={f.pose} intensity={intensity} beat={f.beat} motion={motion} frame={frame} />
    </svg>
  )
}

// Eyes draws the body and both eyes, and moves the eyes between poses.
function Eyes({
  body,
  color,
  pose,
  intensity,
  beat,
  motion,
  frame,
}: {
  body: Body
  color: string
  pose: string
  intensity: number
  beat?: Beat
  motion: Motion
  frame?: Rows
}) {
  const { table, speed, edge } = useTuning()
  const staged = useStages(rowsOf(pose, intensity, table), `${pose}@${intensity}`, beat, motion, table, speed)
  const shown = frame ? { rows: frame, ms: 0 } : staged
  const phase = useState(() => ({ at: (-Math.random() * 5).toFixed(2), every: (4.8 + Math.random() * 2.4).toFixed(2) }))[0]
  const maskId = useState(() => `talkoot-edge-${++masks}`)[0]
  const { eye: E, look } = table
  const travel = (E.H / 2) * Math.max(shown.rows[0].slitLen, 0.35) - 0.6
  const vars = {
    '--t': `${shown.ms / speed}ms`,
    '--phase': `${phase.at}s`,
    '--blink-every': `${phase.every}s`,
    '--wander': `${(look.drift * travel).toFixed(3)}px`,
    '--read': `${look.read}px`,
  }
  const cx = body.cx ?? 12
  return (
    <g class="bob" style={vars}>
      {edge === 'outside' && (
        <defs>
          <mask id={maskId} maskUnits="userSpaceOnUse" x={-12} y={-12} width={48} height={48}>
            <rect x={-12} y={-12} width={48} height={48} fill="white" />
            <g fill="black">{body.el}</g>
          </mask>
        </defs>
      )}
      {/* The outline is the body drawn wider in the theme's text colour, under the fill. */}
      {look.outline > 0 && (
        <g class="outline" stroke-width={look.outline * 2}>
          {body.el}
        </g>
      )}
      <g class="body-fill" fill={color}>
        {body.el}
      </g>
      {/* The edge: a copy of both eyes behind them. One layer behind both
          eyes, or its stroke would cut a pupil from its slit. */}
      {edge !== 'none' && (
        <EyePair rows={shown.rows} cx={cx} cy={body.eyes} E={E} edge={edgeLook(edge, color, look.edge, maskId)} />
      )}
      <EyePair rows={shown.rows} cx={cx} cy={body.eyes} E={E} fill={eyeColor(color)} />
    </g>
  )
}

interface EdgeLook {
  colour: string
  width: number
  mask?: string
}

// edgeLook is the colour and mask of the edge layer. outside takes the colour
// that contrasts with the eye itself, so it reads on either theme.
function edgeLook(edge: Edge, color: string, width: number, maskId: string): EdgeLook {
  if (edge === 'halo') return { colour: 'var(--bg)', width }
  if (edge === 'outside') return { colour: eyeColor(color) === '#ffffff' ? '#1a1a1a' : '#ffffff', width, mask: `url(#${maskId})` }
  return { colour: color, width }
}

function EyePair({ rows, cx: cx0, cy, E, fill, edge }: { rows: Rows; cx: number; cy: number; E: Table['eye']; fill?: string; edge?: EdgeLook }) {
  const paint = edge ? { fill: edge.colour, stroke: edge.colour, strokeWidth: edge.width * 2 } : { fill }
  return (
    <g class={edge ? 'eyes edge' : 'eyes'} style={paint} mask={edge?.mask}>
      {rows.map((r, i) => {
        const cx = cx0 + (i === 0 ? -E.dx : E.dx)
        const h = E.H * r.slitLen
        const travel = (E.H / 2) * Math.max(r.slitLen, 0.35) - 0.6
        const a = (r.pupilAngle * Math.PI) / 180
        return (
          <g key={i} class={i === 0 ? 'eye l' : 'eye r'} style={{ transform: `translate(${cx + r.eyeX}px, ${cy + r.eyeY}px)` }}>
            <g class="blinker">
              <g class="rotor" style={{ transform: `rotate(${r.slitRot + r.tilt}deg)` }}>
                <g class="stretcher">
                  <rect class="slit" x={-E.W / 2} width={E.W} rx={E.W / 2} style={{ height: `${h}px`, y: `${-h / 2}px` }} />
                  <g class="pupil-gate">
                    <g class="wanderer">
                      <g
                        class="pupil"
                        style={{ transform: `translate(0px, ${-r.pupilPos * travel}px) rotate(${r.pupilRot}deg) scale(${r.pupilScale})` }}
                      >
                        {[1, -1].map((side) => (
                          <g
                            key={side}
                            class="bar"
                            style={{
                              // spread 1 moves the bars apart until their tips meet.
                              transform: `translate(${-side * Math.sin(a) * (E.L / 2) * r.spread}px, 0px) rotate(${side * r.pupilAngle}deg)`,
                            }}
                          >
                            <rect x={-E.BW / 2} y={-E.L / 2} width={E.BW} height={E.L} rx={E.BW / 2} />
                          </g>
                        ))}
                      </g>
                    </g>
                  </g>
                </g>
              </g>
            </g>
          </g>
        )
      })}
    </g>
  )
}

// useStages returns the rows to show and how long their transition takes.
// A new pose plays its move, and a new beat plays over the pose and returns
// to it. The browser eases each stage with a CSS transition, and a timer only
// starts the next stage, so no script runs for each frame.
function useStages(
  target: Rows,
  key: string,
  beat: Beat | undefined,
  motion: Motion,
  table: Table,
  speed: number,
): { rows: Rows; ms: number } {
  const [shown, setShown] = useState<{ rows: Rows; ms: number }>({ rows: target, ms: 0 })
  const shownRef = useRef(shown.rows)
  shownRef.current = shown.rows
  const timers = useRef<number[]>([])
  const cancel = () => {
    for (const t of timers.current) clearTimeout(t)
    timers.current = []
  }
  const play = (stages: Stage[]) => {
    cancel()
    let at = 0
    for (const s of stages) {
      const run = () => setShown({ rows: s.rows, ms: s.ms })
      if (at === 0) run()
      else timers.current.push(window.setTimeout(run, at / speed))
      at += s.ms + s.hold
    }
  }
  const first = useRef(true)
  useLayoutEffect(() => {
    if (first.current) {
      first.current = false
      return
    }
    if (motion === 'off') {
      cancel()
      setShown({ rows: target, ms: 0 })
      return
    }
    play(moveStages(shownRef.current, target, table))
    // The key names the target, so the rows need not be compared.
  }, [key, motion === 'off'])
  // A new table is an edit in the studio, and it shows at once.
  const lastTable = useRef(table)
  useLayoutEffect(() => {
    if (lastTable.current === table) return
    lastTable.current = table
    cancel()
    setShown({ rows: target, ms: 0 })
  }, [table])
  const lastBeat = useRef(beat?.key)
  useEffect(() => {
    if (!beat || beat.key === lastBeat.current) return
    lastBeat.current = beat.key
    if (motion === 'off') return
    play(beatStages(beat, shownRef.current, target, table))
  }, [beat?.key])
  useEffect(() => cancel, [])
  return shown
}

// MarkOf draws the mark of a member by id from the team's marks, and nothing
// for a person.
export function MarkOf({ id, size }: { id: string | undefined; size?: number }) {
  const marks = useContext(MarksContext)
  return <MemberMark mark={markOf(marks, id)} size={size} />
}
