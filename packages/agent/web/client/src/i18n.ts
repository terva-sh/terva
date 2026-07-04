// English-as-key runtime i18n for the PWA, mirroring terva's Go i18n layer
// (packages/i18n): t("English source") returns the active-language translation,
// falling back to the English key when there is none. The server advertises its
// active locale in the ctrlproto hello (Hello.locale); the client selects the
// matching bundled catalog. Client-owned strings live here in TS; server-
// originated display text (surface titles, settings labels) is already localized
// server-side before it reaches the wire. See docs/web.md.
//
// Catalog files mirror the Go locale JSON shape: a flat map whose values are
// either a string (a singular translation) or an object of CLDR plural forms
// keyed "one|other" -> {category -> form}. English is implicit — no catalog, so
// keys pass through unchanged (the Go isEN fast path).
//
// Operator overlay for these client strings (server-served overrides layered on
// the bundle) is a later phase; setLocale/mergeOverrides is shaped to accept it.

import fiRaw from './locales/fi.json'

type Singular = Record<string, string>
type Plural = Record<string, Record<string, string>>

function parse(raw: Record<string, unknown>): { singular: Singular; plural: Plural } {
  const singular: Singular = {}
  const plural: Plural = {}
  for (const [k, v] of Object.entries(raw)) {
    if (typeof v === 'string') singular[k] = v
    else if (v && typeof v === 'object') plural[k] = v as Record<string, string>
  }
  return { singular, plural }
}

// Bundled non-English catalogs, keyed by base language. Add a language by
// dropping a locales/<lang>.json next to this file and registering it here.
const CATALOGS: Record<string, { singular: Singular; plural: Plural }> = {
  fi: parse(fiRaw as Record<string, unknown>),
}

let singular: Singular = {}
let plural: Plural = {}
let pluralRules = new Intl.PluralRules('en')
let activeLang = 'en'

// setLocale selects the bundled catalog for a BCP-47 tag ("fi", "fi-FI", …).
// An empty/English/unknown tag leaves the English fast path (keys pass through).
// Returns the resolved base language.
export function setLocale(tag: string): string {
  const base = (tag || 'en').toLowerCase().split('-')[0]
  activeLang = base
  const cat = CATALOGS[base]
  // Copy the bundled catalog: applyServerCatalog overlays onto these maps, and
  // we must never mutate the shared CATALOGS entry.
  singular = { ...(cat?.singular ?? {}) }
  plural = { ...(cat?.plural ?? {}) }
  try {
    pluralRules = new Intl.PluralRules(tag || 'en')
  } catch {
    pluralRules = new Intl.PluralRules('en')
  }
  return base
}

// applyServerCatalog overlays the daemon's effective catalog (its embedded base
// ⊕ the $TERVA_HOME/locales/web overlay) onto the bundled base for its language,
// so an operator's translation edits show after a reload. The bundle stays the
// offline/first-paint fallback; the server copy wins per key.
export function applyServerCatalog(cv: {
  lang?: string
  singular?: Record<string, string>
  plural?: Record<string, Record<string, string>>
}): string {
  const base = setLocale(cv.lang ?? activeLang)
  if (cv.singular) Object.assign(singular, cv.singular)
  if (cv.plural) Object.assign(plural, cv.plural)
  return base
}

export function activeLocale(): string {
  return activeLang
}

// fmt applies Go-style %s/%d/%% substitution, but only when args are supplied —
// so a translated string with a bare '%' (e.g. "100%") is left untouched
// (mirrors the Go i18n format helper).
function fmt(s: string, args: unknown[]): string {
  if (!args.length) return s
  let i = 0
  return s.replace(/%[sd%]/g, (m) => (m === '%%' ? '%' : String(args[i++] ?? '')))
}

// t translates an English source string, falling back to the source. Pass args
// only for strings that carry %-verbs.
export function t(source: string, ...args: unknown[]): string {
  const tr = singular[source]
  return fmt(tr ? tr : source, args)
}

// tn is the plural form: one/other are the English singular/plural templates and
// double as the catalog key ("%d agent|%d agents"). The active locale's CLDR
// category for n selects among the translated forms. With no extra args, n is
// the sole format argument, so tn(k, "%d call", "%d calls") renders directly.
export function tn(n: number, one: string, other: string, ...args: unknown[]): string {
  if (!args.length) args = [n]
  const forms = plural[one + '|' + other]
  if (forms) {
    const form = forms[pluralRules.select(n)] || forms.other || englishPlural(n, one, other)
    return fmt(form, args)
  }
  return fmt(englishPlural(n, one, other), args)
}

function englishPlural(n: number, one: string, other: string): string {
  return n === 1 ? one : other
}
