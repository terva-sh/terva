import { readdirSync, readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { dirname, join, relative } from 'node:path'
import { describe, expect, it } from 'vitest'

// The Playwright smoke suite — 71 files, ~7,600 lines — was outside `tsc` for
// its entire life. tsconfig's `include` named `src` and `vite.config.ts`, and
// nothing anywhere asked whether that covered the package.
//
// Nothing would have. A file outside `include` does not fail a build or a test;
// it simply is not checked, and a fixture in it can go on claiming a wire shape
// the daemon stopped sending. That is the same drift types-shape.test.ts guards
// against on the src side — which the smoke suite's own fakes were exempt from.
//
// So the rule is stated as a CENSUS over the files that exist, not as a list of
// the directories somebody remembered: a new top-level directory of TypeScript
// fails here the day it appears, rather than the day someone notices its
// contents never compiled.
const here = dirname(fileURLToPath(import.meta.url))
const pkgRoot = join(here, '..')

// Directories that hold no source of ours, or hold build OUTPUT of it.
const skipDirs = new Set(['node_modules', 'dist', 'coverage', '.vite', 'test-results', 'playwright-report'])

function typescriptFiles(dir: string): string[] {
  const out: string[] = []
  for (const e of readdirSync(dir, { withFileTypes: true })) {
    if (e.name.startsWith('.') || skipDirs.has(e.name)) continue
    const full = join(dir, e.name)
    if (e.isDirectory()) out.push(...typescriptFiles(full))
    else if (e.name.endsWith('.ts') || e.name.endsWith('.tsx')) out.push(relative(pkgRoot, full))
  }
  return out
}

describe('tsconfig covers every TypeScript file in the package', () => {
  const tsconfig = JSON.parse(readFileSync(join(pkgRoot, 'tsconfig.json'), 'utf8')) as {
    include?: string[]
  }
  const include = tsconfig.include ?? []
  const files = typescriptFiles(pkgRoot)

  it('is looking at a real package', () => {
    // Floors, so a walk that found nothing cannot pass by covering nothing. The
    // suite alone is ~70 files; src is several hundred.
    expect(files.length, 'the file walk found almost nothing — it is scanning the wrong place').toBeGreaterThan(100)
    expect(include.length, 'tsconfig has no include at all').toBeGreaterThan(0)
  })

  // The matcher below understands the two forms this tsconfig uses: a directory
  // name, and an exact file. A glob would be silently mis-handled, so it is an
  // explicit failure rather than a quiet pass.
  it('uses only include patterns this census can evaluate', () => {
    for (const pattern of include) {
      expect(
        /[*?]/.test(pattern),
        `tsconfig include "${pattern}" is a glob, which this census cannot evaluate — ` +
          `teach it the glob, or the coverage check below silently stops meaning anything`,
      ).toBe(false)
    }
  })

  it('leaves no TypeScript file unchecked', () => {
    const covered = (f: string) => include.some((p) => f === p || f.startsWith(p + '/'))
    const orphans = files.filter((f) => !covered(f))
    expect(
      orphans,
      `these files are compiled by nothing — tsc --noEmit never sees them, so a type ` +
        `error in them is invisible to every gate:\n  ${orphans.join('\n  ')}\n` +
        `Add their directory to tsconfig's include.`,
    ).toEqual([])
  })
})
