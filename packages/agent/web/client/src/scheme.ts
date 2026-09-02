// The panel's light/dark preference. A display choice belongs to the browser
// and not the workspace, so this is client-local (localStorage) rather than a
// daemon setting. Stage's theme.ts and the panel's own terva_toolview and
// terva_viewmode have the same shape.
//
// The choice is written to data-scheme on <html>, and styles.css keys its
// palette off that attribute. `auto` is not a third palette. It is the absence
// of an override, so the media query decides, exactly as it did before this
// existed.

import { m } from './i18n'

export type Scheme = 'auto' | 'light' | 'dark'

// The display names are m()-marked for extraction. Translate with tr(name) at
// the render site; the ids are storage values and stay as they are. The glyph
// is what the header button shows for each state.
//
// The sun carries U+FE0E, the text variation selector, written as an escape so
// it survives review as something deliberate rather than invisible whitespace.
// U+2600 is Emoji=Yes but Emoji_Presentation=No, so a browser defaults it to a
// text glyph that takes CSS `color`, which is what makes the birch-tar sun
// possible at all. VS15 states that default rather than relying on it, so a font
// substitution on Windows or iOS cannot hand back a colour emoji that ignores
// the styling. The moon needs no such guard: U+263E is not in Unicode's emoji
// data, so nothing renders it in colour.
export const SCHEMES: { id: Scheme; name: string; glyph: string }[] = [
  { id: 'auto', name: m('Auto (follow system)'), glyph: '◐' },
  { id: 'light', name: m('Light'), glyph: '☀\uFE0E' },
  { id: 'dark', name: m('Dark'), glyph: '☾' },
]

const KEY = 'terva_scheme'
const DEFAULT: Scheme = 'auto'

// The same choice is mirrored into a cookie, because the login page cannot read
// localStorage: it is served under `default-src 'none'` with no script-src at
// all, and being scriptless is deliberate (see login.go — a form that depended
// on the bundle could not be served to a client not yet allowed to fetch the
// bundle). A cookie is the only channel that reaches a server-rendered page
// without widening the CSP on the one page that accepts the bearer token.
//
// This is a MIRROR, not a second source of truth: currentScheme still reads
// localStorage only, so there is nothing to drift. Nothing in the panel reads
// the cookie back.
const COOKIE_MAX_AGE = 60 * 60 * 24 * 365

function mirrorToCookie(scheme: Scheme): void {
  try {
    // Lax, not Strict: Strict withholds the cookie on a cross-site navigation
    // INTO the login page, which is exactly the case that matters — following a
    // link from elsewhere and landing on the form. Secure only under https,
    // because a browser discards a Secure cookie set over plain http, and the
    // loopback deployment is plain http. This mirrors requestIsHTTPS in auth.go.
    const secure = location.protocol === 'https:' ? '; Secure' : ''
    document.cookie = `${KEY}=${scheme}; Path=/; Max-Age=${COOKIE_MAX_AGE}; SameSite=Lax${secure}`
  } catch {
    // Cookies disabled. The panel still themes; the login page follows the OS.
  }
}

function known(id: string | null): Scheme {
  return SCHEMES.some((s) => s.id === id) ? (id as Scheme) : DEFAULT
}

// currentScheme returns the persisted choice, or auto.
export function currentScheme(): Scheme {
  try {
    return known(localStorage.getItem(KEY))
  } catch {
    // A private-mode / storage-blocked browser still follows the system.
    return DEFAULT
  }
}

// applyScheme sets the document's data-scheme, re-skinning live, and persists
// it. Call it before the first render so there is no flash of the wrong
// palette. The attribute is always written, including for auto, so the CSS has
// one shape to match rather than two.
export function applyScheme(id: Scheme): Scheme {
  const scheme = known(id)
  document.documentElement.setAttribute('data-scheme', scheme)
  try {
    localStorage.setItem(KEY, scheme)
  } catch {
    // Ditto: this session is themed, the next one starts from auto.
  }
  // Its own try/catch, so a browser that blocks one store still gets the other.
  mirrorToCookie(scheme)
  return scheme
}

// nextScheme is the cycle order for the header button: auto → light → dark.
export function nextScheme(id: Scheme): Scheme {
  return SCHEMES[(SCHEMES.findIndex((s) => s.id === known(id)) + 1) % SCHEMES.length].id
}

// schemeGlyph and schemeName describe a choice for the button and its tooltip.
export function schemeGlyph(id: Scheme): string {
  return SCHEMES.find((s) => s.id === known(id))!.glyph
}

export function schemeName(id: Scheme): string {
  return SCHEMES.find((s) => s.id === known(id))!.name
}
