import type { TalkootMark } from '../ctrlproto/types'

// A member's mark: one flat shape in one colour with two eye strokes. The
// daemon resolves every member's mark (packages/agent/look), so the client
// only draws it.
//
// ⚠️ MARK_SHAPES and MARK_PALETTE mirror look.Shapes and look.Palette. A Go
// test reads this file and fails when the two differ.

export const MARK_SHAPES = [
  'circle',
  'blob',
  'rounded-square',
  'pill',
  'triangle',
  'hexagon',
  'cloud',
  'drop',
  'tab',
  'shield',
  'gem',
] as const

export const MARK_PALETTE = [
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
] as const

// eyeColor picks the eye strokes that read on a fill: dark on a light colour,
// light on a dark one.
export function eyeColor(fill: string): string {
  const m = /^#([0-9a-f]{2})([0-9a-f]{2})([0-9a-f]{2})$/i.exec(fill)
  if (!m) return '#ffffff'
  const [r, g, b] = [m[1], m[2], m[3]].map((h) => parseInt(h, 16) / 255)
  const lum = 0.2126 * r + 0.7152 * g + 0.0722 * b
  return lum > 0.55 ? '#1a1a1a' : '#ffffff'
}

// markOf finds a member's mark in a map of them. A person, or a member of a
// daemon from before marks, has none.
export function markOf(marks: Record<string, TalkootMark>, id: string | undefined): TalkootMark | undefined {
  if (!id) return undefined
  const m = marks[id]
  return m?.shape && m.color ? m : undefined
}
