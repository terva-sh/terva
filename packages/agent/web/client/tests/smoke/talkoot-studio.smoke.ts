import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { test, expect } from '@playwright/test'

// The Talkoot Avatar Studio (studio.html), on the dev server, because the
// build leaves it out. Three things only a real browser can check:
//   - The transition check. It asks the browser whether each eye point lies in
//     a body's fill, and happy-dom has no geometry. This is the gate that keeps
//     a pose-table edit from spinning, vanishing, touching, or clipping.
//   - The self-test, which plays every pose, beat, and scenario on the page.
//   - Save, through the dev server's own route, into poses.json.

const STUDIO = `http://127.0.0.1:${Number(process.env.SMOKE_STUDIO_PORT ?? 4174)}/studio.html`
const TABLE = new URL('../../src/features/talkoot/face/poses.json', import.meta.url)
const AGREED: string[] = JSON.parse(readFileSync(new URL('../../src/studio/agreed-clips.json', import.meta.url), 'utf8'))

interface Finding {
  kind: string
  from: string
  to: string
  body: string
}

const clipKey = (f: Finding) => `${f.from}${f.to ? ' -> ' + f.to : ', held'} on ${f.body}`

test('the transition check finds no spin, vanish, or touch, and only agreed clips', async ({ page }) => {
  await page.goto(`${STUDIO}?only=transitions`)
  // Thirteen strips from closed show that the page drew the table it checks.
  await expect(page.locator('#strips .strip')).not.toHaveCount(0)
  await page.locator('#check').click()
  const raw = await page.locator('#findings').getAttribute('data-findings')
  const found: Finding[] = JSON.parse(raw ?? 'null')
  expect(found, 'the check did not run').not.toBeNull()

  const faults = found.filter((f) => f.kind !== 'clip').map((f) => `${f.kind} ${f.from} -> ${f.to}`)
  expect(faults, 'a move spins, vanishes, or touches').toEqual([])

  const clips = found.filter((f) => f.kind === 'clip').map(clipKey)
  const agreed = new Set(AGREED)
  expect(
    clips.filter((c) => !agreed.has(c)),
    'a clip that is not in src/studio/agreed-clips.json: fix the pose, or agree the clip in review',
  ).toEqual([])
  // The list only shrinks: a clip that a fix removed leaves the list too.
  expect(
    AGREED.filter((c) => !clips.includes(c)),
    'an agreed clip no longer happens: remove it from src/studio/agreed-clips.json',
  ).toEqual([])
})

test('the self-test plays every pose, beat, presence, and the scenario without an error', async ({ page }) => {
  // The self-test takes about 15 s on a quiet machine. A busy CI runner has
  // run it past 45 s (TKT-01M3SSWY02).
  test.setTimeout(150_000)
  await page.goto(`${STUDIO}?selftest`)
  await expect(page.locator('body')).not.toHaveAttribute('data-selftest', 'running', { timeout: 120_000 })
  await expect(page.locator('body')).toHaveAttribute('data-selftest', 'ok')
  // A positive control: the check ran and drew its findings.
  await expect(page.locator('body')).toHaveAttribute('data-findings', /^\d+$/)
})

test('Save writes poses.json from the studio page, and only from it', async ({ page, request }) => {
  // The page loads twice before the save's own 20 s wait, and all of it
  // shares the test's budget (TKT-01M3SSWY02).
  test.setTimeout(60_000)
  const route = new URL('/__studio/save', STUDIO).toString()
  // Locally the suite reuses a dev server that is already up, which may be
  // another checkout's. Its Save would write that checkout's poses.json, so
  // the test stops unless the server writes this one.
  const target = await (await request.get(route)).text()
  expect(target, `the dev server on ${STUDIO} belongs to another checkout: stop it, or set SMOKE_STUDIO_PORT`).toBe(fileURLToPath(TABLE))
  const before = readFileSync(TABLE, 'utf8')
  await page.goto(`${STUDIO}?only=playground`)
  await page.evaluate(() => localStorage.removeItem('terva-studio-table'))
  await page.reload()
  await page.getByRole('button', { name: 'Save to poses.json' }).click()
  // The save goes through the dev server, which a busy runner can hold past
  // the 5 s default (TKT-01M3SSWY02).
  await expect(page.locator('#note')).toContainText('Saved to src/features/talkoot/face/poses.json', { timeout: 20_000 })
  // The committed table saved unedited writes the same bytes back.
  expect(readFileSync(TABLE, 'utf8')).toBe(before)

  const table = JSON.parse(before)
  const post = (headers: Record<string, string>, data: string) => request.post(route, { headers, data })
  expect((await post({ 'content-type': 'text/plain' }, before)).status(), 'a form post is refused').toBe(415)
  expect((await post({ 'content-type': 'application/json', origin: 'http://elsewhere.example' }, before)).status(), 'another origin is refused').toBe(403)
  const broken = { ...table, poses: { ...table.poses, open: { forms: [] } } }
  expect((await post({ 'content-type': 'application/json' }, JSON.stringify(broken))).status(), 'a table the renderer cannot read is refused').toBe(422)
  expect(readFileSync(TABLE, 'utf8')).toBe(before)
})

test('Reset drops the browser copy, so a later poses.json shows on the next load', async ({ page }) => {
  await page.goto(`${STUDIO}?only=playground`)
  // An edit keeps a copy of the table in the browser.
  await page.locator('#geometry input[data-k="eye gap"]').fill('4')
  await expect.poll(() => page.evaluate(() => localStorage.getItem('terva-studio-table'))).not.toBeNull()
  await page.getByRole('button', { name: 'Reset to defaults' }).click()
  await expect(page.locator('#note')).toContainText('Back to poses.json')
  expect(await page.evaluate(() => localStorage.getItem('terva-studio-table'))).toBeNull()
})
