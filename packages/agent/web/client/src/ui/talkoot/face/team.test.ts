// @vitest-environment happy-dom
import { describe, expect, it } from 'vitest'
import { TEAM_LOOKS, teamIcon, teamLook } from './team'

describe('the team mark', () => {
  it('shows one pose for each team state, and fades only offline', () => {
    expect(teamLook('offline')).toEqual({ pose: 'closed', faded: true })
    expect(teamLook('needs-you').pose).toBe('looking-up')
    expect(teamLook('busy').pose).toBe('focused')
    expect(teamLook('paused').pose).toBe('half-lidded')
    expect(teamLook('online').pose).toBe('open')
    expect(Object.values(TEAM_LOOKS).filter((f) => f.faded)).toHaveLength(1)
  })

  it('reads a missing or unknown state as online', () => {
    expect(teamLook(undefined)).toBe(TEAM_LOOKS.online)
    expect(teamLook('celebrating')).toBe(TEAM_LOOKS.online)
  })

  it('draws a tab icon of the trio in the team colour, with two slits that follow the pose', () => {
    const parse = (s: string) => new DOMParser().parseFromString(s, 'image/svg+xml')
    const open = parse(teamIcon('#12A594', 'online'))
    expect(open.querySelector('parsererror')).toBeNull()
    expect(open.querySelectorAll('circle')).toHaveLength(3)
    expect(open.querySelector('g')!.getAttribute('fill')).toBe('#12A594')
    const slits = open.querySelectorAll('rect')
    expect(slits).toHaveLength(2)
    expect(open.documentElement.getAttribute('opacity')).toBeNull()
    // Closed eyes lie flat, so their slits turn and differ from open ones.
    const closed = parse(teamIcon('#12A594', 'offline'))
    expect(closed.documentElement.getAttribute('opacity')).toBe('0.45')
    expect(closed.querySelector('rect')!.getAttribute('transform')).not.toBe(slits[0].getAttribute('transform'))
  })
})
