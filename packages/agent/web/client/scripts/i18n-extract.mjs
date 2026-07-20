// Extracts the web client's translatable strings — the client-side twin of
// cmd/terva-i18n-lint. It walks src/ with the TypeScript compiler API, finds
// bare t("…") / tn(n, "…", "…") / m("…") calls with literal string arguments,
// and writes the English reference catalogs (English-as-key: singular strings,
// plus {one,other} objects for tn plurals). Files under apps/stage/ route to
// the stage catalog; everything else routes to web — the authoring split the
// daemon merges back together when serving i18n.catalog. A non-literal key
// argument is a hard error — a string must not silently escape the catalog.
// (tr() — the render-site translator for m()-marked table strings — is
// deliberately not extracted and takes any expression.)
//
// It also detects UNWRAPPED user-visible strings — JSX text, user-facing
// attributes (placeholder/title/aria-label/alt), and literal prose passed to
// confirm()/prompt()/alert() — the gap the sync check alone cannot see (it
// only proves the catalog matches the calls that exist). Suppress a finding
// that is intentionally English (a proper noun, a wire token) with an
// `i18n-exempt` comment on the same or the preceding line. Test files are
// exempt from detection.
//
//   node scripts/i18n-extract.mjs               # write the references
//   node scripts/i18n-extract.mjs --check       # fail if stale OR unwrapped strings exist
//   node scripts/i18n-extract.mjs --unwrapped   # list unwrapped findings only (advisory)
//
// See docs/proposals/web-i18n-authoring.md and docs/proposals/i18n-round-2.md.

import ts from 'typescript'
import { readFileSync, writeFileSync, readdirSync, statSync, mkdirSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { dirname, join, relative } from 'node:path'

const here = dirname(fileURLToPath(import.meta.url))
const SRC = join(here, '..', 'src')
const LOCALES = join(here, '..', '..', '..', '..', 'i18n', 'locales')
const OUT = {
  web: join(LOCALES, 'web', 'en.json'),
  stage: join(LOCALES, 'stage', 'en.json'),
}

// catalogOf routes a file (path relative to src/) to its authoring catalog.
function catalogOf(rel) {
  return rel.startsWith('apps/stage/') || rel.startsWith('apps\\stage\\') ? 'stage' : 'web'
}

function walk(dir) {
  const out = []
  for (const name of readdirSync(dir)) {
    const p = join(dir, name)
    if (statSync(p).isDirectory()) out.push(...walk(p))
    else if (/\.tsx?$/.test(p)) out.push(p)
  }
  return out
}

// literalString returns the string value of a node when it is a plain string
// literal or a no-substitution template, else null (the caller errors).
function literalString(node) {
  if (!node) return null
  if (ts.isStringLiteral(node) || ts.isNoSubstitutionTemplateLiteral(node)) return node.text
  return null
}

// prose reports whether a string carries translatable text — at least one run
// of two letters. Filters glyphs ("—", "·", "%"), numbers, and pure markup out
// of the unwrapped-string detector.
function prose(s) {
  return /[A-Za-z]{2}/.test(s)
}

const catalogs = { web: { singular: {}, plural: {} }, stage: { singular: {}, plural: {} } }
const errors = []
const unwrapped = []

// Attributes whose literal values a user reads.
const PROSE_ATTRS = new Set(['placeholder', 'title', 'aria-label', 'alt'])
// Bare/window-qualified calls whose first literal argument a user reads.
const PROSE_CALLS = new Set(['confirm', 'prompt', 'alert'])

for (const file of walk(SRC)) {
  const rel = relative(SRC, file)
  const cat = catalogs[catalogOf(rel)]
  const text = readFileSync(file, 'utf8')
  const lines = text.split('\n')
  const isTest = /\.test\.tsx?$/.test(file)
  const sf = ts.createSourceFile(file, text, ts.ScriptTarget.Latest, true, ts.ScriptKind.TSX)
  const lineOf = (node) => sf.getLineAndCharacterOfPosition(node.getStart()).line
  const at = (node) => `${rel}:${lineOf(node) + 1}`
  // exempt: an `i18n-exempt` comment on the finding's line or the line above.
  const exempt = (node) => {
    const line = lineOf(node)
    return (
      (lines[line] ?? '').includes('i18n-exempt') || (lines[line - 1] ?? '').includes('i18n-exempt')
    )
  }
  const flag = (node, what, sample) => {
    if (isTest || exempt(node)) return
    const clipped = sample.length > 60 ? sample.slice(0, 57) + '…' : sample
    unwrapped.push(`${at(node)}: ${what}: ${JSON.stringify(clipped)}`)
  }
  const visit = (node) => {
    if (ts.isCallExpression(node)) {
      let fn = null
      if (ts.isIdentifier(node.expression)) fn = node.expression.text
      else if (
        ts.isPropertyAccessExpression(node.expression) &&
        ts.isIdentifier(node.expression.expression) &&
        node.expression.expression.text === 'window'
      )
        fn = node.expression.name.text
      if (fn === 't' || fn === 'm') {
        const key = literalString(node.arguments[0])
        if (key === null) errors.push(`${at(node)}: ${fn}() with a non-literal key`)
        else cat.singular[key] = key
      } else if (fn === 'tn') {
        const one = literalString(node.arguments[1])
        const other = literalString(node.arguments[2])
        if (one === null || other === null) errors.push(`${at(node)}: tn() with non-literal forms`)
        else cat.plural[`${one}|${other}`] = { one, other }
      } else if (fn && PROSE_CALLS.has(fn)) {
        const arg = node.arguments[0]
        const lit = literalString(arg)
        if (lit !== null && prose(lit)) flag(node, `unwrapped ${fn}()`, lit)
        else if (arg && ts.isTemplateExpression(arg)) {
          const joined = [arg.head.text, ...arg.templateSpans.map((s) => s.literal.text)].join(' ')
          if (prose(joined)) flag(node, `unwrapped ${fn}() template`, joined)
        }
      }
    } else if (ts.isJsxText(node)) {
      const s = node.text.trim()
      if (s && prose(s)) flag(node, 'unwrapped JSX text', s)
    } else if (ts.isJsxAttribute(node) && node.initializer) {
      const name = node.name.getText(sf)
      if (PROSE_ATTRS.has(name)) {
        const lit = ts.isStringLiteral(node.initializer)
          ? node.initializer.text
          : ts.isJsxExpression(node.initializer)
            ? literalString(node.initializer.expression)
            : null
        if (lit !== null && prose(lit)) flag(node, `unwrapped ${name}=`, lit)
      }
    }
    ts.forEachChild(node, visit)
  }
  visit(sf)
}

if (errors.length) {
  console.error('i18n-extract: non-literal translation calls (wrap the string literally):')
  for (const e of errors) console.error('  ' + e)
  process.exit(1)
}

const unwrappedReport = () => {
  console.error(
    `i18n-extract: ${unwrapped.length} unwrapped user-visible string(s) — wrap in t()/tn(), or mark deliberate English with an i18n-exempt comment:`,
  )
  for (const u of unwrapped) console.error('  ' + u)
}

if (process.argv.includes('--unwrapped')) {
  if (unwrapped.length) {
    unwrappedReport()
    process.exit(1)
  }
  console.log('no unwrapped user-visible strings found')
  process.exit(0)
}

// Emit keys sorted, singular then plural, matching the Go reference's stable
// ordering and its no-HTML-escape, one-entry-per-line style.
function render(cat) {
  const keys = [...Object.keys(cat.singular), ...Object.keys(cat.plural)].sort()
  const lines = keys.map((k) => {
    const v = k in cat.singular ? JSON.stringify(cat.singular[k]) : JSON.stringify(cat.plural[k])
    return `  ${JSON.stringify(k)}: ${v}`
  })
  return { out: '{\n' + lines.join(',\n') + '\n}\n', count: keys.length }
}

if (process.argv.includes('--check')) {
  let stale = false
  for (const [name, path] of Object.entries(OUT)) {
    const { out, count } = render(catalogs[name])
    let current = ''
    try {
      current = readFileSync(path, 'utf8')
    } catch {
      /* missing = stale */
    }
    if (current !== out) {
      console.error(`i18n-extract: ${relative(process.cwd(), path)} is stale — run: npm run i18n-extract`)
      stale = true
    } else {
      console.log(`${name}/en.json is up to date (${count} strings)`)
    }
  }
  if (unwrapped.length) {
    unwrappedReport()
    stale = true
  }
  process.exit(stale ? 1 : 0)
} else {
  for (const [name, path] of Object.entries(OUT)) {
    const { out, count } = render(catalogs[name])
    mkdirSync(dirname(path), { recursive: true })
    writeFileSync(path, out)
    console.log(`wrote ${relative(process.cwd(), path)} (${count} strings)`)
  }
  if (unwrapped.length) unwrappedReport()
}
