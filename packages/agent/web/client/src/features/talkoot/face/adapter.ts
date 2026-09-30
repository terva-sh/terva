import type { TalkootMemberStatus } from '../../../platform/ctrlproto/types'
import type { PlayedBeat } from '../../../platform/talkoot/store'
import type { Face } from '../MemberMark'
import { defaultLook } from './poses'

// faceFor is the face a member's mark shows. The engine's expression wins
// when the status carries one, and the default pose for the presence stands
// in otherwise. The fade always follows the presence: the presence is a fact,
// and the pose is only the engine's reading of it.
//
// side turns the member a glance names into the side the eyes move to. A
// glance at a member the list does not show looks right, as the renderer's
// default does.
export function faceFor(
  status: TalkootMemberStatus | undefined,
  presence: string,
  beat?: PlayedBeat,
  side?: (member: string) => number | undefined,
): Face {
  const look = defaultLook(status, presence)
  const face: Face = status?.expression
    ? { pose: status.expression, intensity: status.intensity ?? 0, faded: look.faded }
    : { pose: look.pose, faded: look.faded }
  if (beat) face.beat = { name: beat.name, key: beat.key, toward: beat.toward ? side?.(beat.toward) : undefined }
  return face
}

// sideIn is the side a member at `from` in a list looks to see `to`: left, -1,
// for a member above it, and right, 1, for one below. A member missing from
// the list, or the member itself, has no side.
export function sideIn(order: readonly string[], from: string, to: string): number | undefined {
  const a = order.indexOf(from)
  const b = order.indexOf(to)
  if (a < 0 || b < 0 || a === b) return undefined
  return b < a ? -1 : 1
}
