// The studio's state: the live pose table, the view settings, and a registry
// that the self-test drives the sections through.

import { createContext } from 'preact'
import { useContext } from 'preact/hooks'
import type { Motion } from '../features/talkoot/face/motion'
import { TABLE, type Table } from '../features/talkoot/face/poses'
import type { Edge } from '../features/talkoot/face/tuning'
import type { Finding } from './check'
import type { Presence } from './data'

export interface Settings {
  theme: 'dark' | 'light'
  motion: Motion
  speed: number
  pupilMin: number
  edge: Edge
  drift: boolean
  read: boolean
}

// StudioApi is how the self-test reaches into each section. A section fills
// in its part when it mounts.
export interface StudioApi {
  showPose?: (id: string) => void
  beat?: (name: string, toward?: number) => void
  presence?: (p: Presence) => void
  scenario?: () => void
  teamState?: (name: string) => void
  teamBody?: (name: string) => void
  gridSize?: (n: number) => void
  runCheck?: () => Finding[]
  strips?: () => number
  roll?: () => boolean
}

export interface Studio {
  table: Table
  // edit changes a copy of the table, and every mark follows.
  edit: (fn: (t: Table) => void) => void
  setTable: (t: Table) => void
  // drop forgets the browser's copy and shows poses.json as committed. It
  // stores nothing, so a later change to the file shows on the next load.
  drop: () => void
  settings: Settings
  set: (s: Partial<Settings>) => void
  // later runs fn after ms, slowed by the speed setting.
  later: (fn: () => void, ms: number) => number
  q: URLSearchParams
  api: StudioApi
}

export const StudioContext = createContext<Studio>(null as unknown as Studio)
export const useStudio = (): Studio => useContext(StudioContext)

// Edits persist in the browser until Save writes the file or Reset drops them.
const KEY = 'terva-studio-table'

export function loadTable(): Table {
  try {
    const t = JSON.parse(localStorage.getItem(KEY) ?? 'null') as Table | null
    if (t?.poses?.open && t.eye && t.look) return t
  } catch {
    // A table that does not parse is dropped, and the committed one shows.
  }
  return structuredClone(TABLE)
}

export function keepTable(t: Table | null) {
  try {
    if (t) localStorage.setItem(KEY, JSON.stringify(t))
    else localStorage.removeItem(KEY)
  } catch {
    // A browser that refuses storage keeps the edits for this page only.
  }
}
