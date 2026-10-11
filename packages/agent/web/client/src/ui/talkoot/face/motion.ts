// How much the Talkoot marks move. A display choice belongs to the browser
// and not the workspace (scheme.ts), so the setting lives in localStorage.
// prefers-reduced-motion forces off, whatever the setting says.

import { useEffect, useState } from 'preact/hooks'
import { m } from '../../../i18n'

export type Motion = 'full' | 'subtle' | 'off'

// The display names are m()-marked for extraction. Translate with tr(name)
// where they render.
export const MOTIONS: { id: Motion; name: string }[] = [
  { id: 'full', name: m('Full motion') },
  { id: 'subtle', name: m('Subtle motion') },
  { id: 'off', name: m('No motion') },
]

const KEY = 'terva_talkoot_motion'
const EVENT = 'terva-talkoot-motion'

const known = (v: string | null): Motion => (v === 'subtle' || v === 'off' ? v : 'full')

// local is the choice made on this page, which wins over what storage holds.
// A browser that refuses storage keeps the choice here, for this page only.
// A change in another tab clears it (useMotion).
let local: Motion | null = null

export function motionSetting(): Motion {
  if (local) return local
  try {
    return known(localStorage.getItem(KEY))
  } catch {
    return 'full'
  }
}

export function setMotionSetting(v: Motion) {
  local = v
  try {
    localStorage.setItem(KEY, v)
  } catch {
    // Storage refuses writes: local holds the choice.
  }
  window.dispatchEvent(new CustomEvent(EVENT))
}

const reducedQuery = () => (typeof matchMedia === 'function' ? matchMedia('(prefers-reduced-motion: reduce)') : null)

// effectiveMotion is the motion the marks show: off when the system asks for
// reduced motion, and the setting otherwise.
export function effectiveMotion(): Motion {
  return reducedQuery()?.matches ? 'off' : motionSetting()
}

// useMotionState is the setting and whether the system asks for reduced
// motion, and it follows a change to either, from this tab or another.
export function useMotionState(): { setting: Motion; reduced: boolean } {
  const read = () => ({ setting: motionSetting(), reduced: !!reducedQuery()?.matches })
  const [v, setV] = useState(read)
  useEffect(() => {
    const update = () => setV(read())
    const fromTab = (e: StorageEvent) => {
      if (e.key !== KEY && e.key !== null) return
      local = null
      update()
    }
    const q = reducedQuery()
    window.addEventListener(EVENT, update)
    window.addEventListener('storage', fromTab)
    q?.addEventListener?.('change', update)
    return () => {
      window.removeEventListener(EVENT, update)
      window.removeEventListener('storage', fromTab)
      q?.removeEventListener?.('change', update)
    }
  }, [])
  return v
}

// useMotion is effectiveMotion, and it follows a change to the setting or to
// the system preference.
export function useMotion(): Motion {
  const { setting, reduced } = useMotionState()
  return reduced ? 'off' : setting
}
