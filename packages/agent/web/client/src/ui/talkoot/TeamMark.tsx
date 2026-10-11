import { t } from '../../i18n'
import type { TeamState } from '../../platform/ctrlproto/types'
import { FaceMark, TEAM_BODY } from './MemberMark'
import { teamLook } from './face/team'

// TEAM_STATE_LABEL names each team state for a person.
export const TEAM_STATE_LABEL: Record<TeamState, () => string> = {
  offline: () => t('offline'),
  'needs-you': () => t('needs you'),
  busy: () => t('busy'),
  paused: () => t('paused'),
  online: () => t('online'),
}

// teamStateLabel names a state. A state the daemon has not sent yet, or one
// this client does not know, has no name, so nothing claims one.
export function teamStateLabel(state: string | undefined): string | undefined {
  return TEAM_STATE_LABEL[state as TeamState]?.()
}

// TeamMark draws a talkoot's own mark: the team body in the team's colour,
// with eyes that show the team's combined state. A daemon from before the
// team mark sends no colour, and then nothing draws.
export function TeamMark({ color, state, size = 24, label }: { color?: string; state?: string; size?: number; label?: string }) {
  if (!color) return null
  return <FaceMark body={TEAM_BODY} shape="team" color={color} size={size} label={label} face={teamLook(state)} />
}
