import { test, expect } from '@playwright/test'
import { installMockBackend, panelSessionURL, SMOKE_SESSION } from './support'

// The session drawer groups its rows busy / idle / cold and collapses cold, so
// opening it while you work shows the working set instead of every 2-message
// probe you ever ran.
//
// The grouping RULE is unit-tested (src/platform/sessionstatus.test.ts) and the
// collapse behaviour is covered in happy-dom (features/sessions/sessions.test.tsx).
// Neither can see the thing this file exists for: happy-dom has no layout, so
// every element is 0×0 and a header that overflows the drawer looks identical to
// one that fits. The drawer is min(320px, 85vw) and its rows already carry five
// controls — three surfaces in this repo have shipped a clipped control with the
// unit suite green, so the geometry gets measured here.

const old = '2020-01-01T00:00:00Z'
const recent = () => new Date(Date.now() - 60_000).toISOString()

const SESSIONS = [
  { id: SMOKE_SESSION, current: true, title: 'the session you are in', model: 'gpt-5.6-sol', messages: 58, usage: { cost_usd: 5.41 } },
  { id: 'working', title: 'a turn is in flight right now', live: true, busy: true, model: 'gpt-5.6-sol', messages: 177, usage: { cost_usd: 8.331 } },
  { id: 'held', title: 'held in memory', live: true, model: 'gpt-5.6-sol', messages: 88, usage: { cost_usd: 4.55 } },
  { id: 'recent', title: 'forgotten but touched minutes ago', updated: recent(), model: 'gpt-5.6-sol', messages: 276, usage: { cost_usd: 42.647 } },
  ...Array.from({ length: 12 }, (_, i) => ({
    id: `cold${i}`,
    title: `Call terva_status and report ${i}`,
    updated: old,
    model: 'gpt-5.6-sol',
    messages: 2,
    usage: { cost_usd: 0.03 },
  })),
]

const mock = (page: import('@playwright/test').Page) =>
  installMockBackend(page, {
    respond: (method) => {
      if (method === 'sessions.list') return { sessions: SESSIONS }
      if (method === 'sessiongroups.list') return { groups: [] }
      if (method === 'personas.list') return { personas: [] }
      return undefined
    },
  })

// rightOverflow returns how far past the drawer's right edge an element reaches.
// A section header is a flex row whose count is pushed to the far end, which is
// exactly where a too-wide header shows up first.
async function rightOverflow(page: import('@playwright/test').Page, selector: string): Promise<number[]> {
  const drawer = (await page.locator('.drawer').boundingBox())!
  const boxes = await page.locator(selector).evaluateAll((els) =>
    els.map((el) => {
      const r = el.getBoundingClientRect()
      return r.right
    }),
  )
  return boxes.map((right) => Math.round(right - (drawer.x + drawer.width))).filter((over) => over > 0)
}

test('panel: the session drawer groups by state and collapses the cold tail', async ({ page }) => {
  await mock(page)
  await page.goto(panelSessionURL)
  await page.locator('.topbar .icon', { hasText: '☰' }).click()
  await expect(page.locator('.drawer')).toBeVisible()

  const busy = page.locator('.session-section--busy')
  const idle = page.locator('.session-section--idle')
  const cold = page.locator('.session-section--cold')

  await expect(busy).toContainText('a turn is in flight right now')
  // The focused session is subscribed, so it is idle by definition and must
  // never be the thing that got hidden. The forgotten-but-recent one sits here
  // too — that is the whole point of the recency rescue.
  await expect(idle).toContainText('the session you are in')
  await expect(idle).toContainText('held in memory')
  await expect(idle).toContainText('forgotten but touched minutes ago')

  // Twelve cold sessions, counted but not rendered.
  await expect(cold.locator('.session-section__count')).toHaveText('12')
  await expect(cold.locator('.session')).toHaveCount(0)

  if (process.env.SS_SHOT) await page.screenshot({ path: `${process.env.SS_SHOT}-collapsed.png` })

  // Nothing may reach past the drawer's right edge — not the headers, not the
  // rows they sit over.
  expect(await rightOverflow(page, '.session-section__head'), 'a section header must fit the drawer').toEqual([])
  expect(await rightOverflow(page, '.session-list .session'), 'a row must fit the drawer').toEqual([])

  await cold.locator('.session-section__head--toggle').click()
  await expect(cold.locator('.session')).toHaveCount(12)
  expect(await rightOverflow(page, '.session-section__head'), 'the expanded header must fit too').toEqual([])
  if (process.env.SS_SHOT) await page.screenshot({ path: `${process.env.SS_SHOT}-expanded.png` })

  // A phone, where the drawer is 85vw and has the least room of all.
  await page.setViewportSize({ width: 390, height: 780 })
  expect(await rightOverflow(page, '.session-section__head'), 'the header must fit a phone drawer').toEqual([])
  expect(await rightOverflow(page, '.session-list .session'), 'a row must fit a phone drawer').toEqual([])
  if (process.env.SS_SHOT) await page.screenshot({ path: `${process.env.SS_SHOT}-phone.png` })
})

// The landing board grows the same groups over its grid, and it is the surface
// where they are most likely to break: each group carries its own .board-grid,
// so a header that did not clear the auto-fill columns would sit on top of the
// tiles. Also the panel's dark palette, which the drawer test does not exercise
// — the header, the caret and the count are the only new colours in this change
// and prefers-color-scheme remaps every one of them.
test('panel: the landing board groups its grid by state, in both palettes', async ({ page }) => {
  await mock(page)
  await page.emulateMedia({ colorScheme: 'dark' })
  await page.goto('/')
  await expect(page.locator('.landing')).toBeVisible()

  const busy = page.locator('.board .session-section--busy')
  const cold = page.locator('.board .session-section--cold')
  await expect(busy.locator('.board-tile')).toHaveCount(1)
  await expect(busy).toContainText('a turn is in flight right now')
  // Each group carries its OWN grid, so the auto-fill columns restart under
  // every header instead of one grid flowing past them. Two while cold is shut
  // — a collapsed group renders no grid at all, not an empty one.
  await expect(page.locator('.board .board-grid')).toHaveCount(2)
  await expect(cold.locator('.board-tile')).toHaveCount(0)
  if (process.env.SS_SHOT) await page.screenshot({ path: `${process.env.SS_SHOT}-board-dark.png` })

  await cold.locator('.session-section__head--toggle').click()
  await expect(page.locator('.board .board-grid')).toHaveCount(3)
  // Thirteen, not twelve: on the landing NO session is focused, so the one the
  // drawer test finds under IDLE (subscribed, therefore live by definition) is
  // cold here. Same list, different surface, and the difference is real.
  await expect(cold.locator('.board-tile')).toHaveCount(13)

  // A tile must not be overlapped by the header of the group below it — the
  // failure mode of stacking three independently-sized grids.
  const headerTop = (await cold.locator('.session-section__head').boundingBox())!.y
  const lastIdleTile = (await page.locator('.board .session-section--idle .board-tile').last().boundingBox())!
  expect(headerTop, 'the cold header must clear the idle grid above it').toBeGreaterThanOrEqual(
    lastIdleTile.y + lastIdleTile.height,
  )

  await page.emulateMedia({ colorScheme: 'light' })
  if (process.env.SS_SHOT) await page.screenshot({ path: `${process.env.SS_SHOT}-board-light.png` })
})

// The ordinary case in focus mode against a freshly restarted daemon: nothing is
// live and nothing is recent, so cold IS the list. Collapsing it there would
// leave a drawer that has hidden everything it had.
test('panel: cold opens when it is the only group with anything in it', async ({ page }) => {
  await installMockBackend(page, {
    respond: (method) => {
      if (method === 'sessions.list') {
        return {
          sessions: [
            { id: 'cold-a', title: 'an old session', updated: old, messages: 2, usage: { cost_usd: 0 } },
            { id: 'cold-b', title: 'another old session', updated: old, messages: 2, usage: { cost_usd: 0 } },
          ],
        }
      }
      if (method === 'sessiongroups.list') return { groups: [] }
      if (method === 'personas.list') return { personas: [] }
      return undefined
    },
  })
  await page.goto('/')
  await expect(page.locator('.landing')).toBeVisible()
  await page.locator('.topbar .icon', { hasText: '☰' }).click()

  await expect(page.locator('.drawer')).toBeVisible()
  await expect(page.locator('.session-section--cold .session')).toHaveCount(2)
  await expect(page.locator('.session-section--busy')).toHaveCount(0)
  await expect(page.locator('.session-section--idle')).toHaveCount(0)
  if (process.env.SS_SHOT) await page.screenshot({ path: `${process.env.SS_SHOT}-all-cold.png` })
})
