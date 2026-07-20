// Stage theme presets — named palettes that swap the --stage-* CSS variables
// (defined per-theme in stage.css under :root[data-theme="…"]). The choice is a
// client-local preference (Stage owns its own shell/theme), applied to the
// document root so both the library and the chat screens re-skin at once.

import { m } from '../../i18n'

// The display names are m()-marked for extraction; translate with tr(name) at
// the render site (the ids are wire/storage values and stay as-is).
export type ThemePreset = { id: string; name: string }

export const THEMES: ThemePreset[] = [
  { id: 'dusk', name: m('Dusk') },
  { id: 'parchment', name: m('Parchment') },
  { id: 'nocturne', name: m('Nocturne') },
  { id: 'rose', name: m('Rose') },
]

const KEY = 'stage-theme'
const DEFAULT = 'dusk'

function known(id: string | null): string {
  return THEMES.some((t) => t.id === id) ? (id as string) : DEFAULT
}

// currentTheme returns the persisted preset id, or the default.
export function currentTheme(): string {
  try {
    return known(localStorage.getItem(KEY))
  } catch {
    return DEFAULT
  }
}

// applyTheme sets the document's data-theme (re-skinning live) and persists it.
export function applyTheme(id: string): void {
  const theme = known(id)
  document.documentElement.setAttribute('data-theme', theme)
  try {
    localStorage.setItem(KEY, theme)
  } catch {
    // A private-mode / storage-blocked browser still themes for this session.
  }
}
