import type { TeamState } from '../../../platform/ctrlproto/types'
import { eyeColor } from '../../../platform/talkoot/marks'
import type { Face } from '../MemberMark'
import { EYE, rowsOf } from './poses'

// TEAM_LOOKS is what the team mark's eyes show for each team state. The team
// has no mood of its own, so each state has one fixed pose, and only an
// offline team fades.
export const TEAM_LOOKS: Record<TeamState, Face> = {
  offline: { pose: 'closed', faded: true },
  'needs-you': { pose: 'looking-up' },
  busy: { pose: 'focused' },
  paused: { pose: 'half-lidded' },
  online: { pose: 'open' },
}

// teamLook is the face for a state. With no state, or one this client does not
// know, the eyes rest open. That is the online pose too, and no label claims
// the state (teamStateLabel).
export function teamLook(state: string | undefined): Face {
  return TEAM_LOOKS[state as TeamState] ?? TEAM_LOOKS.online
}

// The trio in SVG markup, for a favicon. It matches TEAM_BODY in MemberMark.
const TRIO = '<circle cx="9.5" cy="13.5" r="7.5"/><circle cx="16.8" cy="8.2" r="4.9"/><circle cx="18.3" cy="16.8" r="4.2"/>'
const EYES_X = 9.8
const EYES_Y = 13.2

// teamIcon is the team mark as a standalone SVG, for the tab's icon. A
// favicon is 16 to 32 pixels, below the pupil's size, so the slits alone
// carry the pose, as they do on a small mark. It is drawn with attributes
// only, because a page's stylesheet does not reach an icon.
export function teamIcon(color: string, state: string | undefined): string {
  const look = teamLook(state)
  const eyes = rowsOf(look.pose)
    .map((r, i) => {
      const h = Math.max(EYE.H * r.slitLen, EYE.W)
      const x = EYES_X + (i === 0 ? -EYE.dx : EYE.dx) + r.eyeX
      const y = EYES_Y + r.eyeY
      const turn = r.slitRot + r.tilt
      return `<rect x="${f(-EYE.W / 2)}" y="${f(-h / 2)}" width="${f(EYE.W)}" height="${f(h)}" rx="${f(EYE.W / 2)}" transform="translate(${f(x)} ${f(y)}) rotate(${f(turn)})"/>`
    })
    .join('')
  const fade = look.faded ? ' opacity="0.45"' : ''
  return (
    `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24"${fade}>` +
    `<g fill="${color}" stroke="#1a1a1a" stroke-width="1.2" paint-order="stroke">${TRIO}</g>` +
    `<g fill="${eyeColor(color)}">${eyes}</g></svg>`
  )
}

const f = (n: number) => String(Math.round(n * 100) / 100)
