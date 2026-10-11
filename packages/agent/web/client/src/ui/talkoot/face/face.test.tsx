// @vitest-environment happy-dom
import { act, cleanup, render } from '@testing-library/preact'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { BODIES, FaceMark, MemberMark } from '../MemberMark'
import { motionSetting, setMotionSetting } from './motion'
import { BEATS, beatStages, defaultLook, moveStages, needsShut, POSES, rowsAt, rowsOf, SHUT_SHARE, strongForms, TABLE, type Rows, type Table } from './poses'
import { DEFAULT_TUNING, TuningContext } from './tuning'

const mark = { shape: 'hexagon', color: '#3E63DD' }

// The poses and beats docs/proposals/talkoot-members.md names, and the poses
// with a strong form.
const HELD = ['open', 'focused', 'looking-up', 'half-lidded', 'closed-squint', 'worried', 'frustrated', 'skeptical', 'closed']
const BEAT_NAMES = ['happy', 'glance', 'slow-blink', 'fast-blink']
const STRONG = ['looking-up', 'worried', 'frustrated', 'skeptical', 'focused']

const reducedMotion = (on: boolean) =>
  vi.stubGlobal('matchMedia', (q: string) => ({
    matches: on && q.includes('reduce'),
    media: q,
    addEventListener: () => {},
    removeEventListener: () => {},
  }))

// shown reads the rows a rendered mark draws now, from its transforms.
function shown(el: Element) {
  const eyes = [...el.querySelectorAll('.eyes:not(.edge) .eye')]
  return eyes.map((e) => ({
    eye: (e as HTMLElement).style.transform,
    rotor: (e.querySelector('.rotor') as HTMLElement).style.transform,
    slit: (e.querySelector('.slit') as unknown as HTMLElement).style.height,
  }))
}

beforeEach(() => {
  localStorage.clear()
  reducedMotion(false)
  // The page keeps its own choice over storage, so each test starts at full.
  setMotionSetting('full')
})
afterEach(() => {
  cleanup()
  vi.useRealTimers()
  vi.unstubAllGlobals()
})

describe('the pose table', () => {
  it('holds every held pose and beat the proposal names, and the strong forms', () => {
    expect([...POSES].sort()).toEqual([...HELD].sort())
    expect([...BEATS].sort()).toEqual([...BEAT_NAMES].sort())
    for (const p of HELD) expect(strongForms(p)).toBe(STRONG.includes(p) ? 1 : 0)
  })

  it('draws the strongest form at or below the intensity it gets', () => {
    expect(rowsOf('worried', 0)).toEqual(rowsOf('worried'))
    expect(rowsOf('worried', 1)).not.toEqual(rowsOf('worried', 0))
    // A later engine can send a level this renderer does not draw yet.
    expect(rowsOf('worried', 7)).toEqual(rowsOf('worried', 1))
    expect(rowsOf('open', 3)).toEqual(rowsOf('open', 0))
    // A pose this renderer does not know draws open.
    expect(rowsOf('dreaming')).toEqual(rowsOf('open'))
  })

  it('mirrors the right eye of a pose that gives only the left', () => {
    const [l, r] = rowsOf('worried')
    expect(r.tilt).toBe(-l.tilt)
  })
})

describe('the 45-degree rule', () => {
  const turn = (r: { slitRot: number; tilt: number }) => r.slitRot + r.tilt
  // No visible slit may turn more than 45 degrees away from both of its ends.
  const spins = (a: string, b: string) => {
    const from = rowsOf(a)
    const to = rowsOf(b)
    for (let i = 0; i <= 40; i++) {
      const rows = rowsAt(from, to, i / 40)
      for (const [k, r] of rows.entries()) {
        if (r.slitLen < 0.05) continue
        if (Math.min(Math.abs(turn(r) - turn(from[k])), Math.abs(turn(r) - turn(to[k]))) > 45) return true
      }
    }
    return false
  }

  it('lets no visible slit spin between any two poses, in either direction', () => {
    for (const a of HELD) for (const b of HELD) expect(spins(a, b), `${a} -> ${b}`).toBe(false)
  })

  it('shuts a slit that would turn too far, and eases one that would not', () => {
    expect(needsShut(rowsOf('focused'), rowsOf('closed'))).toBe(true)
    expect(needsShut(rowsOf('closed'), rowsOf('open'))).toBe(true)
    expect(needsShut(rowsOf('open'), rowsOf('worried'))).toBe(false)
    const half = rowsAt(rowsOf('focused'), rowsOf('closed'), SHUT_SHARE)
    expect(half.every((r) => r.slitLen === 0 && r.pupilScale === 0)).toBe(true)
    expect(moveStages(rowsOf('focused'), rowsOf('closed'))).toHaveLength(3)
    expect(moveStages(rowsOf('open'), rowsOf('worried'))).toHaveLength(1)
  })
})

describe('beats', () => {
  it('play over the pose and end on the held pose', () => {
    const held = rowsOf('open')
    for (const name of BEAT_NAMES) {
      const stages = beatStages({ name, key: 1 }, held, held)
      expect(stages.length, name).toBeGreaterThan(1)
      expect(stages[stages.length - 1].rows).toEqual(held)
    }
    const left = beatStages({ name: 'glance', key: 1, toward: -1 }, held, held)
    expect(left[0].rows.every((r) => r.eyeX < 0)).toBe(true)
    expect(beatStages({ name: 'dance', key: 1 }, held, held)).toEqual([])
  })

  it('follow the 45-degree rule into the beat and back to any held pose', () => {
    // No stage that takes time may turn a slit past 45 degrees: a beat such as
    // happy over closed shuts, turns, and opens, as a move does.
    for (const pose of HELD)
      for (const name of BEAT_NAMES) {
        const held = rowsOf(pose)
        let at: Rows = held
        for (const s of beatStages({ name, key: 1 }, held, held)) {
          if (s.ms > 0) expect(needsShut(at, s.rows), `${name} over ${pose}`).toBe(false)
          at = s.rows
        }
      }
  })
})

describe('the tuning', () => {
  it('draws with the table the context gives, and follows an edit at once', () => {
    const wide: Table = { ...TABLE, eye: { ...TABLE.eye, dx: 5 }, look: { ...TABLE.look, outline: 1.2, read: 0.8 } }
    const drawn = (t: Table) => (
      <TuningContext.Provider value={{ ...DEFAULT_TUNING, table: t }}>
        <MemberMark mark={mark} size={32} face={{ pose: 'open' }} />
      </TuningContext.Provider>
    )
    const { container, rerender } = render(drawn(TABLE))
    const svg = container.querySelector('svg')!
    const before = shown(svg)[0].eye
    // The committed look: an outline 0.8 wide on each side, and a reading
    // reach of 0.45, the value the prototype approved.
    expect(svg.querySelector('.outline')?.getAttribute('stroke-width')).toBe('1.6')
    expect((svg.querySelector('.bob') as unknown as HTMLElement).style.getPropertyValue('--read')).toBe('0.45px')
    rerender(drawn(wide))
    expect(shown(svg)[0].eye).not.toBe(before)
    expect(svg.querySelector('.outline')?.getAttribute('stroke-width')).toBe('2.4')
    expect((svg.querySelector('.bob') as unknown as HTMLElement).style.getPropertyValue('--read')).toBe('0.8px')
  })

  it('draws a frame still, whatever the motion', () => {
    const frame = rowsOf('closed')
    const { container } = render(<FaceMark body={BODIES.circle} color="#3E63DD" size={32} face={{ pose: 'open' }} frame={frame} />)
    const svg = container.querySelector('svg')!
    expect(svg.classList.contains('still')).toBe(true)
    expect(shown(svg).every((e) => e.rotor.includes('rotate(90'))).toBe(true)
  })
})

describe('the motion setting', () => {
  it('keeps the choice for the page when storage refuses it', () => {
    vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => {
      throw new Error('quota')
    })
    setMotionSetting('off')
    expect(motionSetting()).toBe('off')
    vi.restoreAllMocks()
  })

  it('follows a change made in another tab over the choice of this page', () => {
    setMotionSetting('subtle')
    const { container } = render(<MemberMark mark={mark} size={32} face={{ pose: 'open' }} />)
    const svg = container.querySelector('svg')!
    expect(svg.classList.contains('motion-subtle')).toBe(true)
    act(() => {
      localStorage.setItem('terva_talkoot_motion', 'off')
      window.dispatchEvent(new StorageEvent('storage', { key: 'terva_talkoot_motion', newValue: 'off' }))
    })
    expect(svg.classList.contains('motion-off')).toBe(true)
    expect(motionSetting()).toBe('off')
  })
})

describe('the default look', () => {
  it('shows the presence, and the most serious pause', () => {
    expect(defaultLook(undefined, 'idle')).toEqual({ pose: 'open', faded: false })
    expect(defaultLook({ idle: true }, 'idle')).toEqual({ pose: 'closed', faded: false })
    expect(defaultLook(undefined, 'working').pose).toBe('focused')
    expect(defaultLook(undefined, 'waiting').pose).toBe('looking-up')
    expect(defaultLook(undefined, 'offline')).toEqual({ pose: 'closed', faded: true })
    expect(defaultLook({ pauses: ['person'] }, 'paused').pose).toBe('half-lidded')
    expect(defaultLook({ pauses: ['person', 'spend'] }, 'paused').pose).toBe('closed-squint')
    expect(defaultLook({ pauses: ['person', 'spend', 'failed'] }, 'paused').pose).toBe('worried')
  })
})

describe('the mark', () => {
  it('draws the slit and pupil eye, an edge, and an outline, and drops the pupil below 24 pixels', () => {
    const { container, rerender } = render(<MemberMark mark={mark} size={24} face={{ pose: 'open' }} />)
    const svg = container.querySelector('svg')!
    expect(svg.querySelectorAll('.eyes:not(.edge) .slit')).toHaveLength(2)
    expect(svg.querySelectorAll('.eyes:not(.edge) .pupil .bar')).toHaveLength(4)
    expect((svg.querySelector('.eyes.edge') as unknown as HTMLElement).style.fill.toLowerCase()).toMatch(/#3e63dd|rgb\(62, 99, 221\)/)
    expect(svg.querySelector('.outline')).toBeTruthy()
    expect(svg.classList.contains('tiny')).toBe(false)
    rerender(<MemberMark mark={mark} size={16} face={{ pose: 'worried', intensity: 1 }} />)
    expect(svg.classList.contains('tiny')).toBe(true)
    // A strong form has no brow to draw without a pupil, so it draws its base.
    expect(svg.getAttribute('data-intensity')).toBeNull()
  })

  it('draws every held pose, each in its own way', () => {
    const drawn = new Set<string>()
    for (const pose of HELD) {
      const { container, unmount } = render(<MemberMark mark={mark} size={32} face={{ pose }} />)
      expect(container.querySelector('svg')!.getAttribute('data-pose')).toBe(pose)
      drawn.add(JSON.stringify(shown(container.querySelector('svg')!)))
      unmount()
    }
    expect(drawn.size).toBe(HELD.length)
  })

  it('moves past 45 degrees through a shut eye', () => {
    vi.useFakeTimers()
    const { container, rerender } = render(<MemberMark mark={mark} size={32} face={{ pose: 'focused' }} />)
    const svg = container.querySelector('svg')!
    rerender(<MemberMark mark={mark} size={32} face={{ pose: 'closed' }} />)
    expect(shown(svg).every((e) => e.slit === '0px')).toBe(true)
    act(() => {
      vi.advanceTimersByTime(1000)
    })
    const end = shown(svg)
    expect(end.every((e) => e.slit !== '0px' && e.rotor.includes('rotate(90'))).toBe(true)
  })

  it('plays a beat and returns to the held pose', () => {
    vi.useFakeTimers()
    const { container, rerender } = render(<MemberMark mark={mark} size={32} face={{ pose: 'open' }} />)
    const svg = container.querySelector('svg')!
    const before = shown(svg)
    rerender(<MemberMark mark={mark} size={32} face={{ pose: 'open', beat: { name: 'glance', key: 1, toward: 1 } }} />)
    expect(shown(svg)).not.toEqual(before)
    act(() => {
      vi.advanceTimersByTime(3000)
    })
    expect(shown(svg)).toEqual(before)
  })

  it('shows poses without moving when motion is off, or when the system asks for reduced motion', () => {
    for (const setup of [() => setMotionSetting('off'), () => reducedMotion(true)]) {
      localStorage.clear()
      reducedMotion(false)
      setup()
      vi.useFakeTimers()
      const { container, rerender, unmount } = render(<MemberMark mark={mark} size={32} face={{ pose: 'focused' }} />)
      const svg = container.querySelector('svg')!
      expect(svg.classList.contains('motion-off')).toBe(true)
      rerender(<MemberMark mark={mark} size={32} face={{ pose: 'closed', beat: { name: 'happy', key: 1 } }} />)
      // The new pose shows at once, with no stage in between and no beat.
      expect(shown(svg).every((e) => e.rotor.includes('rotate(90'))).toBe(true)
      unmount()
      vi.useRealTimers()
    }
  })

  it('keeps loops to full motion, and a mark with no face still', () => {
    setMotionSetting('subtle')
    const { container } = render(
      <>
        <MemberMark mark={mark} size={32} face={{ pose: 'focused' }} />
        <MemberMark mark={mark} size={32} />
      </>,
    )
    const [live, still] = [...container.querySelectorAll('svg')]
    expect(live.classList.contains('motion-subtle')).toBe(true)
    expect(live.classList.contains('loop-focus')).toBe(true)
    expect(still.classList.contains('still')).toBe(true)
    expect(still.getAttribute('data-pose')).toBe('open')
  })
})
