// Extracts the web client's translatable strings — the client-side twin of
// cmd/terva-i18n-lint. It walks src/ with the TypeScript compiler API, finds
// bare t("…") / tn(n, "…", "…") calls with literal string arguments, and writes
// the English reference catalog packages/i18n/locales/web/en.json (English-as-
// key: singular strings, plus {one,other} objects for tn plurals). A non-literal
// key argument is a hard error — a string must not silently escape the catalog.
//
//   node scripts/i18n-extract.mjs           # write the reference
//   node scripts/i18n-extract.mjs --check   # fail if the reference is stale
//
// See docs/proposals/web-i18n-authoring.md.

import ts from 'typescript'
import { readFileSync, writeFileSync, readdirSync, statSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { dirname, join, relative } from 'node:path'

const here = dirname(fileURLToPath(import.meta.url))
const SRC = join(here, '..', 'src')
const OUT = join(here, '..', '..', '..', '..', 'i18n', 'locales', 'web', 'en.json')

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

const singular = {}
const plural = {}
const errors = []

for (const file of walk(SRC)) {
  const text = readFileSync(file, 'utf8')
  const sf = ts.createSourceFile(file, text, ts.ScriptTarget.Latest, true, ts.ScriptKind.TSX)
  const at = (node) => {
    const { line } = sf.getLineAndCharacterOfPosition(node.getStart())
    return `${relative(SRC, file)}:${line + 1}`
  }
  const visit = (node) => {
    if (ts.isCallExpression(node) && ts.isIdentifier(node.expression)) {
      const fn = node.expression.text
      if (fn === 't') {
        const key = literalString(node.arguments[0])
        if (key === null) errors.push(`${at(node)}: t() with a non-literal key`)
        else singular[key] = key
      } else if (fn === 'tn') {
        const one = literalString(node.arguments[1])
        const other = literalString(node.arguments[2])
        if (one === null || other === null) errors.push(`${at(node)}: tn() with non-literal forms`)
        else plural[`${one}|${other}`] = { one, other }
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

// Emit keys sorted, singular then plural, matching the Go reference's stable
// ordering and its no-HTML-escape, one-entry-per-line style.
const keys = [...Object.keys(singular), ...Object.keys(plural)].sort()
const lines = keys.map((k) => {
  const v = k in singular ? JSON.stringify(singular[k]) : JSON.stringify(plural[k])
  return `  ${JSON.stringify(k)}: ${v}`
})
const out = '{\n' + lines.join(',\n') + '\n}\n'

if (process.argv.includes('--check')) {
  let current = ''
  try {
    current = readFileSync(OUT, 'utf8')
  } catch {
    /* missing = stale */
  }
  if (current !== out) {
    console.error(`i18n-extract: ${relative(process.cwd(), OUT)} is stale — run: npm run i18n-extract`)
    process.exit(1)
  }
  console.log(`web/en.json is up to date (${keys.length} strings)`)
} else {
  writeFileSync(OUT, out)
  console.log(`wrote ${relative(process.cwd(), OUT)} (${keys.length} strings)`)
}
