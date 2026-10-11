import { describe, expect, it } from 'vitest'
import { faceFor, sideIn } from './adapter'

describe('the face a member shows', () => {
  it('shows the engine\'s expression at its intensity, and the default pose without one', () => {
    expect(faceFor({ member: 'jev', presence: 'idle', expression: 'frustrated', intensity: 1 }, 'idle')).toEqual({
      pose: 'frustrated',
      intensity: 1,
      faded: false,
    })
    expect(faceFor({ member: 'jev', presence: 'working' }, 'working')).toEqual({ pose: 'focused', faded: false })
    expect(faceFor({ member: 'jev', presence: 'idle', expression: '' }, 'idle')).toEqual({ pose: 'open', faded: false })
  })

  it('keeps the fade of the presence under an expression', () => {
    expect(faceFor({ member: 'jev', presence: 'offline', expression: 'worried' }, 'offline')).toEqual({
      pose: 'worried',
      intensity: 0,
      faded: true,
    })
  })

  it('plays the newest beat, with a glance turned to the side of the member it names', () => {
    const order = ['helm', 'jev', 'gage']
    const side = (to: string) => sideIn(order, 'jev', to)
    expect(faceFor(undefined, 'idle', { name: 'glance', key: 4, toward: 'helm' }, side).beat).toEqual({ name: 'glance', key: 4, toward: -1 })
    expect(faceFor(undefined, 'idle', { name: 'glance', key: 5, toward: 'gage' }, side).beat).toEqual({ name: 'glance', key: 5, toward: 1 })
    expect(faceFor(undefined, 'idle', { name: 'happy', key: 6 }, side).beat).toEqual({ name: 'happy', key: 6, toward: undefined })
    expect(faceFor(undefined, 'idle').beat).toBeUndefined()
  })

  it('gives no side for the member itself or one the list does not show', () => {
    expect(sideIn(['helm', 'jev'], 'jev', 'jev')).toBeUndefined()
    expect(sideIn(['helm', 'jev'], 'jev', 'nobody')).toBeUndefined()
    expect(sideIn(['helm', 'jev'], 'nobody', 'helm')).toBeUndefined()
  })
})
