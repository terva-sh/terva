// The Talkoot Avatar Studio: a development page that designs, tunes, and
// checks member faces on the product's own renderer (TKT-01M3NSM90G). The
// dev server serves it as studio.html, and the build leaves it out.

import { useEffect, useMemo, useRef, useState } from 'preact/hooks'
import type { Motion } from '../features/talkoot/face/motion'
import { TABLE, type Table } from '../features/talkoot/face/poses'
import { TuningContext, type Edge, type Tuning } from '../features/talkoot/face/tuning'
import { Grid, Sidebar, Team } from './gallery'
import { Playground } from './playground'
import { selftest } from './selftest'
import { keepTable, loadTable, StudioContext, useStudio, type Settings, type Studio as StudioState, type StudioApi } from './state'
import { Tiles } from './tiles'
import { Clips, Transitions } from './transitions'

const SECTIONS: [string, () => preact.JSX.Element][] = [
  ['playground', Playground],
  ['animations', Tiles],
  ['transitions', Transitions],
  ['clips', Clips],
  ['poses', Grid],
  ['sidebar', Sidebar],
  ['team', Team],
]

const MOTIONS: Motion[] = ['full', 'subtle', 'off']
const EDGES: Edge[] = ['body', 'halo', 'outside', 'none']
const EDGE_NAMES: Record<Edge, string> = { body: 'body colour', halo: 'background colour', outside: 'halo outside the body', none: 'none' }
const SPEEDS = [4, 2, 1, 0.5, 0.25, 0.1]

function initialSettings(q: URLSearchParams): Settings {
  const motion = q.get('motion') as Motion
  const edge = q.get('edge') as Edge
  const speed = Number(q.get('speed'))
  return {
    theme: q.get('theme') === 'light' ? 'light' : 'dark',
    motion: MOTIONS.includes(motion) ? motion : 'full',
    // The self-test plays the scenario at four times speed, so it ends sooner.
    speed: speed > 0 ? speed : q.has('selftest') ? 4 : 1,
    pupilMin: 24,
    edge: EDGES.includes(edge) ? edge : 'body',
    drift: true,
    read: true,
  }
}

export function Studio() {
  const q = useMemo(() => new URLSearchParams(location.search), [])
  const [table, setTableState] = useState<Table>(() => {
    const t = loadTable()
    const w = Number(q.get('edgeW'))
    return w > 0 ? { ...t, look: { ...t.look, edge: w } } : t
  })
  const [settings, setSettings] = useState(() => initialSettings(q))
  const speed = useRef(settings.speed)
  speed.current = settings.speed
  const api = useMemo<StudioApi>(() => ({}), [])

  const setTable = (t: Table) => {
    keepTable(t)
    setTableState(t)
  }
  const studio: StudioState = {
    table,
    setTable,
    drop: () => {
      keepTable(null)
      setTableState(structuredClone(TABLE))
    },
    edit: (fn) => {
      const t = structuredClone(table)
      fn(t)
      setTable(t)
    },
    settings,
    set: (s) => setSettings((x) => ({ ...x, ...s })),
    later: (fn, ms) => window.setTimeout(fn, ms / speed.current),
    q,
    api,
  }
  const latest = useRef(studio)
  latest.current = studio

  const tuning = useMemo<Tuning>(
    () => ({ table, speed: settings.speed, pupilMin: settings.pupilMin, motion: settings.motion, edge: settings.edge, drift: settings.drift, read: settings.read }),
    [table, settings],
  )

  useEffect(() => {
    document.body.classList.remove('dark', 'light')
    document.body.classList.add(settings.theme)
  }, [settings.theme])

  // Speed slows the CSS loops too, through each animation's playback rate.
  useEffect(() => {
    const id = window.setInterval(() => {
      for (const a of document.getAnimations()) if (a instanceof CSSAnimation) a.playbackRate = speed.current
    }, 250)
    return () => clearInterval(id)
  }, [])

  useEffect(() => {
    if (location.hash) requestAnimationFrame(() => document.querySelector(location.hash)?.scrollIntoView())
    if (q.has('selftest')) selftest(() => latest.current)
  }, [])

  const only = q.get('only')
  return (
    <StudioContext.Provider value={studio}>
      <TuningContext.Provider value={tuning}>
        <Header />
        <main>
          {SECTIONS.filter(([id]) => !only || id === only).map(([id, View]) => (
            <View key={id} />
          ))}
        </main>
      </TuningContext.Provider>
    </StudioContext.Provider>
  )
}

function Header() {
  const { table, edit, settings, set } = useStudio()
  const reduced = typeof matchMedia === 'function' && matchMedia('(prefers-reduced-motion: reduce)').matches
  const num = (e: Event) => Number((e.target as HTMLInputElement).value)
  return (
    <header>
      <h1>Talkoot Avatar Studio</h1>
      <span>
        <label>Theme</label>{' '}
        <select value={settings.theme} onChange={(e) => set({ theme: (e.target as HTMLSelectElement).value as Settings['theme'] })}>
          <option>dark</option>
          <option>light</option>
        </select>
      </span>
      <span>
        <label>Motion</label>{' '}
        <select value={settings.motion} onChange={(e) => set({ motion: (e.target as HTMLSelectElement).value as Motion })}>
          {MOTIONS.map((m) => (
            <option key={m}>{m}</option>
          ))}
        </select>
      </span>
      {reduced && (
        <span>
          <label>prefers-reduced-motion is on, so motion is off</label>
        </span>
      )}
      <span>
        <label>Transition</label> <input type="range" min={80} max={1200} value={table.ms} onInput={(e) => edit((t) => void (t.ms = num(e)))} />{' '}
        <span>{table.ms}ms</span>
      </span>
      <span>
        <label>Outline</label>{' '}
        <input type="range" min={0} max={2} step={0.1} value={table.look.outline} onInput={(e) => edit((t) => void (t.look.outline = num(e)))} />{' '}
        <span>{table.look.outline}</span>
      </span>
      <span>
        <label>
          <input type="checkbox" checked={settings.drift} onChange={(e) => set({ drift: (e.target as HTMLInputElement).checked })} /> Idle drift
        </label>{' '}
        <input type="range" min={0.05} max={0.6} step={0.05} value={table.look.drift} style={{ width: '80px' }} onInput={(e) => edit((t) => void (t.look.drift = num(e)))} />{' '}
        <span>{table.look.drift.toFixed(2)}</span>
      </span>
      <span>
        <label>
          <input type="checkbox" checked={settings.read} onChange={(e) => set({ read: (e.target as HTMLInputElement).checked })} /> Reading
        </label>{' '}
        <input type="range" min={0.1} max={1.5} step={0.05} value={table.look.read} style={{ width: '80px' }} onInput={(e) => edit((t) => void (t.look.read = num(e)))} />{' '}
        <span>{table.look.read.toFixed(2)}</span>
      </span>
      <span>
        <label>Eye edge</label>{' '}
        <select id="edge" value={settings.edge} onChange={(e) => set({ edge: (e.target as HTMLSelectElement).value as Edge })}>
          {EDGES.map((m) => (
            <option key={m} value={m}>
              {EDGE_NAMES[m]}
            </option>
          ))}
        </select>{' '}
        <input type="range" min={0.1} max={1.5} step={0.05} value={table.look.edge} style={{ width: '70px' }} onInput={(e) => edit((t) => void (t.look.edge = num(e)))} />{' '}
        <span>{table.look.edge.toFixed(2)}</span>
      </span>
      <span>
        <label>Shut to turn past</label>{' '}
        <input type="range" min={10} max={180} step={5} value={table.turnLimit} style={{ width: '80px' }} onInput={(e) => edit((t) => void (t.turnLimit = num(e)))} />{' '}
        <span>{table.turnLimit}°</span>
      </span>
      <span>
        <label>Speed</label>{' '}
        <select value={String(settings.speed)} onChange={(e) => set({ speed: Number((e.target as HTMLSelectElement).value) })}>
          {(SPEEDS.includes(settings.speed) ? SPEEDS : [...SPEEDS, settings.speed]).map((s) => (
            <option key={s} value={String(s)}>
              {s}x
            </option>
          ))}
        </select>
      </span>
      <span>
        <label>Pupil hides below</label>{' '}
        <input type="number" min={8} max={96} value={settings.pupilMin} style={{ width: '52px' }} onInput={(e) => set({ pupilMin: num(e) || 24 })} /> px
      </span>
    </header>
  )
}
