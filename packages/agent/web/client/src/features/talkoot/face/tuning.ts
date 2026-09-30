// The tuning a face draws with. The product draws with the committed table and
// the defaults below. The Talkoot Avatar Studio (studio.html) provides its own
// tuning, so every mark on its page follows an edit as the edit happens.

import { createContext } from 'preact'
import { useContext } from 'preact/hooks'
import type { Motion } from './motion'
import { TABLE, type Table } from './poses'

// Edge is the layer behind both eyes. body, the design's choice, draws it in
// the body's colour, so an eye that crosses the body's edge carries a patch of
// face with it. halo draws it in the background's colour, and outside draws a
// contrasting halo outside the body only. none leaves it out.
export type Edge = 'body' | 'halo' | 'outside' | 'none'

export interface Tuning {
  table: Table
  // speed scales every move, beat, and loop: 0.25 plays at a quarter speed.
  speed: number
  // pupilMin is the smallest size, in pixels, that draws the pupil. Below it
  // an x on a slit of three pixels is a blur, so the slit carries the pose.
  pupilMin: number
  // motion replaces the browser's setting. Reduced motion still wins.
  motion?: Motion
  edge: Edge
  drift: boolean
  read: boolean
}

export const DEFAULT_TUNING: Tuning = { table: TABLE, speed: 1, pupilMin: 24, edge: 'body', drift: true, read: true }

export const TuningContext = createContext<Tuning>(DEFAULT_TUNING)

export const useTuning = (): Tuning => useContext(TuningContext)
