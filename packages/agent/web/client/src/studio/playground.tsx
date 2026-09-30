// The playground: one large mark, every control that changes it, the sliders
// that edit the pose table, and Save.

import { useEffect, useRef, useState } from 'preact/hooks'
import { BODIES, FaceMark, type Face } from '../features/talkoot/MemberMark'
import { defaultLook, rowsOf, TABLE, type Row, type Table } from '../features/talkoot/face/poses'
import { formatTable, NAME, validTable } from './table'
import { formId, formsOf, parseForm, type FormRef } from './check'
import { BEAT_BUTTONS, GEO, PRESENCES, RANGES } from './data'
import { keepTable, useStudio } from './state'
import { Buttons, Row as Line, Section, Slider, Swatches } from './ui'

let beats = 0

// SCENARIO is a turn that goes wrong, played through a toy engine. Each change
// names its cause, as the engine's trace will.
const SCENARIO: [number, string, string, string, (p: Play) => void][] = [
  [0, 'presence idle', 'open', 'presence', (p) => p.show('open', false)],
  [1200, 'envelope from atlas', 'fast-blink', 'line 88, message to jev while idle', (p) => p.beat('fast-blink')],
  [2200, 'presence working', 'focused', 'turn 12 started', (p) => p.show('focused')],
  [4200, 'tool_error', '(held)', 'turn 12, edit failed, 1 of 3', () => {}],
  [5200, 'tool_error', '(held)', 'turn 12, edit failed, 2 of 3', () => {}],
  [6200, 'tool_error', 'frustrated', 'turn 12, tool_error x3 in one turn', (p) => p.show('frustrated')],
  [8400, 'provider retry', 'worried', 'turn 12, retry 1 of 4, seed 0x5f3a picked worried over frustrated', (p) => p.show('worried')],
  [10400, 'retry ok', 'focused', 'turn 12, retry succeeded', (p) => p.show('focused')],
  [12000, 'handoff sent', 'glance right', 'line 91, handoff to atlas', (p) => p.beat('glance', 1)],
  [13600, 'handoff picked up', 'happy', 'line 93, atlas claimed the handoff', (p) => p.beat('happy')],
  [15600, 'presence paused', 'closed-squint', 'pause kinds [spend], spend cap tripped', (p) => p.show('closed-squint')],
  [18000, 'presence idle', 'open', 'a person resumed jev', (p) => p.show('open')],
  [20000, 'idle stop', 'closed, not faded', 'worker stopped after idle_stop', (p) => p.show('closed', false)],
  [22400, 'presence offline', 'closed, faded', 'the talkoot run ended', (p) => p.fade(true)],
]

interface Play {
  show: (id: string, faded?: boolean) => void
  beat: (name: string, toward?: number) => void
  fade: (f: boolean) => void
}

const rnd = (a: number, b: number) => a + Math.random() * (b - a)
const round = (r: Row): Row => Object.fromEntries(Object.entries(r).map(([k, v]) => [k, Math.round(v * 100) / 100])) as unknown as Row

// rollRow makes a random eye within sensible ranges: flat or upright, with a
// brow or a plain pupil.
function rollRow(): Row {
  const flat = Math.random() < 0.3
  const brow = Math.random() < 0.5
  const r = rowsOf('open')[0]
  Object.assign(r, { slitLen: rnd(0.4, 1.1), slitRot: flat ? 90 : 0, eyeY: rnd(-0.8, 1.4), tilt: Math.round(rnd(-20, 20)) })
  if (brow) Object.assign(r, { pupilPos: rnd(1.5, 2.2), pupilScale: 1, spread: rnd(0.3, 1), pupilAngle: Math.round(rnd(45, 90)), pupilRot: Math.round(rnd(-25, 25)) })
  else Object.assign(r, { pupilPos: flat ? 0 : rnd(-0.6, 0.8), pupilScale: flat ? 0 : rnd(0.6, 1) })
  return round(r)
}


export function Playground() {
  const studio = useStudio()
  const { table, edit, setTable, drop, settings, later, q, api } = studio
  const [body, setBody] = useState(() => (q.get('body') && BODIES[q.get('body')!] ? q.get('body')! : 'hexagon'))
  const [color, setColor] = useState('#3E63DD')
  const [size, setSize] = useState(160)
  const [sel, setSel] = useState<FormRef>(() => parseForm(q.get('pose') ?? '', table) ?? { pose: 'open', intensity: 0 })
  const [face, setFace] = useState<Face>(() => ({ pose: sel.pose, intensity: sel.intensity }))
  const [jump, setJump] = useState(0)
  const [which, setWhich] = useState<'both' | 'l' | 'r'>('both')
  const [from, setFrom] = useState('open')
  const [to, setTo] = useState('closed')
  const [log, setLog] = useState<string[]>([])
  const [keepName, setKeepName] = useState('')
  const [note, setNote] = useState('')
  const [text, setText] = useState(() => formatTable(table))
  const t0 = useRef(Date.now())

  useEffect(() => setText(formatTable(table)), [table])

  const forms = formsOf(table)
  const ids = forms.map(formId)
  const current = table.poses[sel.pose]?.forms[sel.intensity] ?? table.poses.open.forms[0]

  const show = (id: string, faded = false) => {
    const f = parseForm(id, table)
    if (!f) return
    setSel(f)
    setFace((x) => ({ ...x, pose: f.pose, intensity: f.intensity, faded }))
  }
  const beat = (name: string, toward?: number) => setFace((x) => ({ ...x, beat: { name, key: ++beats, toward } }))
  const fade = (faded: boolean) => setFace((x) => ({ ...x, faded }))
  const say = (signal: string, result: string, cause: string) => {
    const s = ((Date.now() - t0.current) / 1000).toFixed(1).padStart(6)
    setLog((l) => [`${s}s  ${signal} -> ${result}  cause: ${cause}`, ...l].slice(0, 200))
  }
  const presence = (name: string) => {
    const p = PRESENCES.find((x) => x.name === name)!
    const look = defaultLook(p.status, p.presence)
    show(look.pose, look.faded)
    say(p.name, look.pose + (look.faded ? ', faded' : ''), 'presence set by hand')
  }
  const scenario = () => {
    setLog([])
    t0.current = Date.now()
    const play: Play = { show, beat, fade }
    for (const [at, signal, result, cause, act] of SCENARIO)
      later(() => {
        act(play)
        say(signal, result, cause)
      }, at)
  }
  const playMove = () => {
    const f = parseForm(from, table)
    if (!f) return
    setSel(f)
    setFace({ pose: f.pose, intensity: f.intensity })
    setJump((j) => j + 1)
    later(() => show(to), 400)
  }
  const cycle = () => ids.forEach((id, i) => later(() => show(id), i * (table.ms + 900)))

  // The rows of the form on show, for the readout and the sliders.
  const [l, r] = rowsOf(sel.pose, sel.intensity, table)
  const src = which === 'r' ? r : l
  const editRow = (k: keyof Row, v: number) =>
    edit((t) => {
      const f = t.poses[sel.pose].forms[sel.intensity]
      if (which === 'both') {
        f.l = { ...rowsOf(sel.pose, sel.intensity, t)[0], [k]: v }
        f.r = null
        return
      }
      if (!f.r) f.r = rowsOf(sel.pose, sel.intensity, t)[1]
      if (which === 'l') f.l = { ...rowsOf(sel.pose, sel.intensity, t)[0], [k]: v }
      else f.r = { ...f.r, [k]: v }
    })
  const roll = () => {
    const eye = rollRow()
    const right = Math.random() < 0.25 ? { ...eye, slitLen: Math.round(rnd(0.4, 1.1) * 100) / 100, pupilAngle: Math.round(rnd(45, 90)) } : null
    const t = structuredClone(table)
    t.poses.rolled = { forms: [{ l: eye, r: right }] }
    setTable(t)
    setSel({ pose: 'rolled', intensity: 0 })
    setFace({ pose: 'rolled' })
    return true
  }
  const keep = () => {
    const name = keepName.trim()
    if (!NAME.test(name)) return setNote('A pose name is lower-case letters, digits, and hyphens.')
    if (table.poses[name]) return setNote(`${name} exists. Pick a new name.`)
    edit((t) => {
      t.poses[name] = structuredClone(t.poses[sel.pose])
      if (sel.pose === 'rolled') delete t.poses.rolled
    })
    setSel({ pose: name, intensity: 0 })
    setFace({ pose: name })
    setKeepName('')
    setNote('')
  }
  const remove = () => {
    if (TABLE.poses[sel.pose]) return setNote('A pose in poses.json stays. Reset to defaults restores its numbers.')
    edit((t) => {
      delete t.poses[sel.pose]
    })
    show('open')
  }
  const copy = () => {
    void navigator.clipboard?.writeText(formatTable(table))
    setNote('Copied the pose table.')
  }
  const reset = () => {
    drop()
    show('open')
    setNote('Back to poses.json as committed.')
  }
  const save = async () => {
    const bad = validTable(table)
    if (bad) return setNote(`Not saved: ${bad}`)
    setNote('Saving…')
    try {
      const res = await fetch('/__studio/save', { method: 'POST', headers: { 'content-type': 'application/json' }, body: JSON.stringify(table) })
      const msg = await res.text()
      if (!res.ok) return setNote(`Not saved: ${msg}`)
      // The file now holds these numbers, so the browser's copy is not needed.
      keepTable(null)
      setNote('Saved to src/features/talkoot/face/poses.json. Commit it.')
    } catch (e) {
      setNote(`Not saved: ${(e as Error).message}. Save needs the dev server (npm run dev).`)
    }
  }
  const pasteTable = (value: string) => {
    let t: Table
    try {
      t = JSON.parse(value) as Table
    } catch (e) {
      return setNote(`Not valid JSON: ${(e as Error).message}`)
    }
    const bad = validTable(t)
    if (bad) return setNote(`Not a pose table: ${bad}`)
    setTable(t)
    setNote('')
  }

  api.showPose = (id) => show(id)
  api.beat = beat
  api.presence = (p) => presence(p.name)
  api.scenario = scenario
  api.roll = roll

  const fmt = (o: Row) =>
    Object.entries(o)
      .map(([k, v]) => `${k.padEnd(10)} ${v.toFixed(2)}`)
      .join('\n')

  return (
    <Section
      id="playground"
      title="Playground"
      note="Pick a body, a colour, and a pose or a beat. The sliders edit the pose that is showing, and every mark on the page follows the edit. Edits persist in this browser until Save writes them to poses.json or Reset drops them."
    >
      <div class="play">
        <div>
          <div class="stage" id="stage">
            <FaceMark key={jump} body={BODIES[body]} shape={body} color={color} size={size} face={face} />
          </div>
          <Line k="Size">
            <input type="range" min={12} max={200} value={size} onInput={(e) => setSize(Number((e.target as HTMLInputElement).value))} />
            <span>{size}px</span>
          </Line>
          <div class="readout">{`${formId(sel)}\n\nleft\n${fmt(l)}\n\nright\n${fmt(r)}`}</div>
        </div>
        <div>
          <Line k="Body">
            <Buttons names={Object.keys(BODIES)} on={(n) => n === body} pick={setBody} />
          </Line>
          <Line k="Colour">
            <Swatches pick={setColor} />
            <input type="color" value={color} onInput={(e) => setColor((e.target as HTMLInputElement).value)} />
          </Line>
          <Line k="Held pose">
            <Buttons names={ids} on={(n) => n === formId(sel)} pick={(n) => show(n)} />
          </Line>
          <Line k="Beat">
            <Buttons names={BEAT_BUTTONS.map((b) => b.name)} pick={(n) => {
              const b = BEAT_BUTTONS.find((x) => x.name === n)!
              beat(b.beat, b.toward)
            }} />
          </Line>
          <Line k="Presence">
            <Buttons names={PRESENCES.map((p) => p.name)} pick={presence} />
          </Line>
          <Line k="Transition">
            from
            <select value={from} onChange={(e) => setFrom((e.target as HTMLSelectElement).value)}>
              {ids.map((n) => (
                <option key={n}>{n}</option>
              ))}
            </select>
            to
            <select value={to} onChange={(e) => setTo((e.target as HTMLSelectElement).value)}>
              {ids.map((n) => (
                <option key={n}>{n}</option>
              ))}
            </select>
            <button onClick={playMove}>Play</button>
            <button onClick={cycle}>Cycle every pose</button>
          </Line>
          <Line k="Scenario">
            <button onClick={scenario}>Play a turn that goes wrong</button>
            <span class="hint">Each change names its cause, as the engine's trace would.</span>
          </Line>
          <div class="log">
            {log.map((line, i) => (
              <div key={log.length - i}>{line}</div>
            ))}
          </div>
        </div>
        <div class="sliders">
          <Line k="Editing">
            <select value={which} onChange={(e) => setWhich((e.target as HTMLSelectElement).value as 'both' | 'l' | 'r')}>
              <option value="both">both eyes, mirrored</option>
              <option value="l">left eye</option>
              <option value="r">right eye</option>
            </select>
          </Line>
          <div id="sliders">
            {(Object.keys(RANGES) as (keyof Row)[]).map((k) => {
              const [min, max, step] = RANGES[k]
              return <Slider key={k} name={k} min={min} max={max} step={step} value={src[k]} set={(v) => editRow(k, v)} />
            })}
          </div>
          <Line k="Pose">
            <button onClick={roll}>Roll a pose</button>
            <input value={keepName} placeholder="name" style={{ width: '110px' }} onInput={(e) => setKeepName((e.target as HTMLInputElement).value)} />
            <button onClick={keep}>Keep as</button>
            <button onClick={remove}>Delete pose</button>
          </Line>
          <Line k="Eye shape">
            <span class="hint">shared by every pose</span>
          </Line>
          <div id="geometry">
            {(Object.keys(GEO) as (keyof typeof GEO)[]).map((k) => {
              const [name, min, max, step] = GEO[k]
              return <Slider key={k} name={name} min={min} max={max} step={step} value={table.eye[k]} set={(v) => edit((t) => void (t.eye[k] = v))} />
            })}
          </div>
          <Line k="Loop">
            <select
              value={current.loop ?? ''}
              onChange={(e) => {
                const loop = (e.target as HTMLSelectElement).value
                edit((t) => {
                  const f = t.poses[sel.pose].forms[sel.intensity]
                  if (loop) f.loop = loop
                  else delete f.loop
                })
              }}
            >
              <option value="">none</option>
              <option>focus</option>
              <option>lookup</option>
              <option>skeptic</option>
            </select>
          </Line>
          <Line k="Table">
            <button class="primary" onClick={() => void save()}>
              Save to poses.json
            </button>
            <button onClick={copy}>Copy pose table</button>
            <button onClick={reset}>Reset to defaults</button>
          </Line>
          <div class="note" id="note">
            {note}
          </div>
          <textarea spellcheck={false} value={text} onInput={(e) => setText((e.target as HTMLTextAreaElement).value)} onChange={(e) => pasteTable((e.target as HTMLTextAreaElement).value)} />
          <div class="hint">{settings.motion === 'off' ? 'Motion is off, so moves show at once.' : ''}</div>
        </div>
      </div>
    </Section>
  )
}
