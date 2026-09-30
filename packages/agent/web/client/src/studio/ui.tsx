// Small pieces the studio's sections share.

import { render, type ComponentChildren } from 'preact'
import type { Body } from '../features/talkoot/MemberMark'
import type { Inside } from './check'
import { PALETTE } from './data'

export function Buttons({ names, on, pick }: { names: string[]; on?: (n: string) => boolean; pick: (n: string) => void }) {
  return (
    <span class="buttons">
      {names.map((n) => (
        <button key={n} class={on?.(n) ? 'on' : undefined} onClick={() => pick(n)}>
          {n}
        </button>
      ))}
    </span>
  )
}

export function Swatches({ pick }: { pick: (c: string) => void }) {
  return (
    <span class="buttons">
      {PALETTE.map((c) => (
        <button key={c} class="swatch" style={{ background: c }} title={c} onClick={() => pick(c)} />
      ))}
    </span>
  )
}

export function Row({ k, children }: { k: string; children: ComponentChildren }) {
  return (
    <div class="row">
      <span class="k">{k}</span>
      {children}
    </div>
  )
}

export function Section({ id, title, note, children }: { id: string; title: string; note?: ComponentChildren; children: ComponentChildren }) {
  return (
    <section id={id}>
      <h2>{title}</h2>
      {note && <p>{note}</p>}
      {children}
    </section>
  )
}

export function Slider({
  name,
  min,
  max,
  step,
  value,
  set,
}: {
  name: string
  min: number
  max: number
  step: number
  value: number
  set: (v: number) => void
}) {
  return (
    <div class="s">
      <span>{name}</span>
      <input type="range" min={min} max={max} step={step} value={value} data-k={name} onInput={(e) => set(Number((e.target as HTMLInputElement).value))} />
      <span>{value.toFixed(2)}</span>
    </div>
  )
}

const NS = 'http://www.w3.org/2000/svg'
const probes = new WeakMap<Record<string, Body>, Inside>()

// insideOf answers whether a point lies in a body's fill. It draws each body
// once into a hidden SVG and asks the browser, because a path's fill has no
// simpler test. happy-dom has no geometry, so this runs in a real browser.
export function insideOf(bodies: Record<string, Body>): Inside {
  const known = probes.get(bodies)
  if (known) return known
  const svg = document.createElementNS(NS, 'svg')
  svg.setAttribute('width', '0')
  svg.setAttribute('height', '0')
  svg.setAttribute('aria-hidden', 'true')
  svg.style.position = 'absolute'
  document.body.appendChild(svg)
  const shapes: Record<string, SVGGeometryElement> = {}
  for (const [name, b] of Object.entries(bodies)) {
    const g = document.createElementNS(NS, 'g')
    svg.appendChild(g)
    render(b.el, g)
    shapes[name] = g.firstElementChild as SVGGeometryElement
  }
  const inside: Inside = (body, x, y) => shapes[body].isPointInFill(new DOMPoint(x, y))
  probes.set(bodies, inside)
  return inside
}
