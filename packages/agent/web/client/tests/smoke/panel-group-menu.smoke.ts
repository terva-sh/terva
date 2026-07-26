import { test, expect } from '@playwright/test'
import { installMockBackend } from './support'

// The per-session group menu on the landing, which used to be cut in half.
//
// It hangs to the LEFT of its button so it doesn't push past the tile's right
// edge — but the button is the first of four in a right-aligned row, so on the
// LEFTMOST tile of the grid the menu reached ~38px past the board's left edge.
// The board is a scrolling column, and `overflow-y: auto` computes overflow-x to
// `auto` as well, so that overhang wasn't merely overhanging: it was clipped
// away. "No groups yet" rendered as "roups yet".
//
// happy-dom cannot see this — it has no layout, so every element is at 0×0 and
// every placement looks identical. The arithmetic is unit-tested (nudge, in
// src/features/sessions/groupmenu.test.ts); this measures the real thing.
const SESSIONS = Array.from({ length: 8 }, (_, i) => ({
  id: String.fromCharCode(97 + i),
  title: `session ${i}`,
  model: 'gpt-5.6-sol',
  messages: i + 2,
}))

const GROUPS = [
  { id: 'g1', name: 'Stage', color: '#c88', members: ['a'] },
  { id: 'g2', name: 'Review', color: '#8c8', members: [] },
]

// outsideCorners returns the corners of the menu at which a click does NOT land
// on the menu — i.e. the parts a user cannot see or reach. Hit-testing is the
// honest probe: a clipped element still reports its full bounding box, so
// measuring the box alone would prove nothing.
async function outsideCorners(page: import('@playwright/test').Page): Promise<string[]> {
  const box = (await page.locator('.groupmenu-pop').boundingBox())!
  return page.evaluate((b) => {
    // Inset past the 8px border-radius: hit-testing follows the rounded shape,
    // so a point 2px into a corner misses the menu even when it is fully on
    // screen. 12px is inside the paint, and still 38px short of the overhang
    // this test exists to catch.
    const i = 12
    const corners: Record<string, [number, number]> = {
      'top-left': [b.x + i, b.y + i],
      'top-right': [b.x + b.width - i, b.y + i],
      'bottom-left': [b.x + i, b.y + b.height - i],
      'bottom-right': [b.x + b.width - i, b.y + b.height - i],
    }
    return Object.entries(corners)
      .filter(([, [x, y]]) => !document.elementFromPoint(x, y)?.closest('.groupmenu-pop'))
      .map(([name]) => name)
  }, box)
}

test('panel: the group menu stays inside the board it drops out of', async ({ page }) => {
  await installMockBackend(page, {
    respond: (method) => {
      if (method === 'sessions.list') return { sessions: SESSIONS }
      if (method === 'personas.list') return { personas: [] }
      if (method === 'sessiongroups.list') return { groups: GROUPS }
      return undefined
    },
  })

  await page.goto('/')
  await expect(page.locator('.landing')).toBeVisible()

  const menus = page.locator('.board-tile .groupmenu > button')
  const pop = page.locator('.groupmenu-pop')

  // The leftmost tile is the failing case: this is where the menu ran out of
  // board. Assert it is shifted AND that the shift lands it fully on screen —
  // a shift that overcorrected would still fail the corner probe.
  await menus.first().click()
  await expect(pop).toBeVisible()
  await expect(pop).toContainText('Stage')
  expect(await pop.evaluate((el) => el.style.transform), 'the leftmost menu must be nudged back inside').not.toBe('')
  expect(await outsideCorners(page), 'every corner of the menu must be clickable').toEqual([])
  if (process.env.GM_SHOT) await page.screenshot({ path: `${process.env.GM_SHOT}-leftmost.png` })

  // The rightmost tile has room and must be left alone — otherwise "nudge
  // everything right" would pass the test above while breaking placement.
  await page.keyboard.press('Escape')
  await page.locator('.landing-body').click({ position: { x: 4, y: 4 } })
  await menus.nth(3).click()
  await expect(pop).toBeVisible()
  expect(await pop.evaluate((el) => el.style.transform), 'a menu with room must not move').toBe('')
  expect(await outsideCorners(page)).toEqual([])

  // A phone, where the grid is a single column and the board is the whole width.
  await page.setViewportSize({ width: 390, height: 780 })
  await page.locator('.landing-body').click({ position: { x: 4, y: 4 } })
  await menus.first().click()
  await expect(pop).toBeVisible()
  expect(await outsideCorners(page), 'the menu must fit on a phone too').toEqual([])
})
