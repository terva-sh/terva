// The studio's fixed data: the team-body candidates, the palette, the
// presences it can show, and the slider ranges. The pose table is not here. It
// is poses.json, which the renderer reads and Save writes.

import { TEAM_BODY, type Body } from '../features/talkoot/MemberMark'
import { TEAM_LOOKS } from '../features/talkoot/face/team'
import type { Row } from '../features/talkoot/face/poses'

// TEAM_BODIES are the candidate team-only bodies from the prototype. Each is
// a group, not one of the member shapes. TKT-01M3NSM9RP picks one for the
// product.
export const TEAM_BODIES: Record<string, Body> = {
  cluster: {
    el: <path d="M4 20a4 4 0 0 1-1.5-7.7 4.5 4.5 0 0 1 5.5-5.3 5 5 0 0 1 8 0 4.5 4.5 0 0 1 5.5 5.3A4 4 0 0 1 20 20z" />,
    eyes: 14,
  },
  // The product's body, so the studio shows what the product draws.
  trio: TEAM_BODY,
  stack: {
    el: (
      <>
        <rect x="6" y="1.5" width="16" height="15" rx="5" />
        <rect x="2" y="6" width="17" height="16" rx="5" />
      </>
    ),
    eyes: 14,
    cx: 10.5,
  },
  crowd: {
    el: (
      <>
        <g transform="rotate(-18 5 12)">
          <circle cx="5" cy="6" r="2.8" />
          <rect x="4" y="6" width="2" height="6" rx="1" />
        </g>
        <circle cx="12" cy="4.4" r="3.1" />
        <rect x="11" y="5" width="2" height="7" rx="1" />
        <g transform="rotate(18 19 12)">
          <circle cx="19" cy="6" r="2.8" />
          <rect x="18" y="6" width="2" height="6" rx="1" />
        </g>
        <rect x="2" y="10.5" width="20" height="11.5" rx="5.5" />
      </>
    ),
    eyes: 15.8,
  },
}

export const PALETTE = [
  '#E5484D',
  '#F76B15',
  '#FFB224',
  '#A18072',
  '#46A758',
  '#12A594',
  '#00A2C7',
  '#3E63DD',
  '#6E56CF',
  '#AB4ABA',
  '#D6409F',
  '#8D8D8D',
  '#5B8C3A',
]

// A Presence is a member state the studio can show, as the daemon reports it.
// The face comes from the product's defaultLook, so the studio shows what the
// sidebar shows.
export interface Presence {
  name: string
  presence: string
  status: { pauses?: string[]; idle?: boolean }
  label: string
}

export const PRESENCES: Presence[] = [
  { name: 'idle', presence: 'idle', status: {}, label: 'idle' },
  { name: 'working', presence: 'working', status: {}, label: 'working: edit' },
  { name: 'waiting', presence: 'waiting', status: {}, label: 'waiting on you' },
  { name: 'paused person', presence: 'paused', status: { pauses: ['person'] }, label: 'paused by you' },
  { name: 'paused limit', presence: 'paused', status: { pauses: ['spend'] }, label: 'paused: spend cap' },
  { name: 'paused broken', presence: 'paused', status: { pauses: ['failed'] }, label: 'paused: failed' },
  { name: 'idle stopped', presence: 'idle', status: { idle: true }, label: 'idle, stopped' },
  { name: 'offline', presence: 'offline', status: {}, label: 'offline' },
]

// TEAM_STATES are the team's combined states, as the product draws them.
export const TEAM_STATES: Record<string, { pose: string; faded?: boolean }> = TEAM_LOOKS

export const NAMES = ['Helm', 'Jev', 'Atlas', 'Rune', 'Kivi', 'Sade', 'Oras', 'Tuli']
export const SHAPES = ['hexagon', 'shield', 'blob', 'drop', 'tab', 'triangle', 'pill', 'circle']

// RANGES are each row number's slider: minimum, maximum, and step.
export const RANGES: Record<keyof Row, [number, number, number]> = {
  slitLen: [0, 1.6, 0.01],
  slitRot: [-90, 90, 1],
  eyeX: [-3, 3, 0.05],
  eyeY: [-3, 3, 0.05],
  pupilPos: [-2.2, 2.2, 0.01],
  pupilScale: [0, 2, 0.01],
  tilt: [-45, 45, 1],
  spread: [0, 2, 0.01],
  pupilAngle: [0, 90, 1],
  pupilRot: [-45, 45, 1],
}

// GEO are the eye-shape sliders, which every pose shares.
export const GEO: Record<'dx' | 'H' | 'W' | 'L' | 'BW', [string, number, number, number]> = {
  dx: ['eye gap', 1.5, 5, 0.05],
  H: ['slit height', 2, 9, 0.05],
  W: ['slit width', 0.5, 3, 0.05],
  L: ['pupil bar', 1, 6, 0.05],
  BW: ['pupil width', 0.3, 2.5, 0.05],
}

export const BEAT_BUTTONS: { name: string; beat: string; toward?: number }[] = [
  { name: 'happy', beat: 'happy' },
  { name: 'glance left', beat: 'glance', toward: -1 },
  { name: 'glance right', beat: 'glance', toward: 1 },
  { name: 'slow-blink', beat: 'slow-blink' },
  { name: 'fast-blink', beat: 'fast-blink' },
]
