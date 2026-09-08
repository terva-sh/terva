import { test, expect } from '@playwright/test'
import { installMockBackend, panelSessionURL, SMOKE_SESSION } from './support'

// Restoring an archived session was impossible once the archive grew past the
// drawer, which is about eight entries at a laptop height.
//
// .drawer-archive is a footer under .session-list, and .session-list is flex:1
// with flex-basis 0, so it has no height of its own to give back. Past that
// point the archive block grew straight off the bottom of a .drawer that is
// height:100% and does not scroll. Measured at 900x620 with 18 entries before
// the fix: the last Restore sat 645px below the fold, and the only ancestors
// reporting scroll room (.drawer, .drawer-scrim) are overflow:visible, so they
// report room they will never let anyone scroll through. Twelve wheel events of
// 400px moved it exactly 0px.
//
// The decisive assertion here is the click. Playwright scrolls a target into
// view the way a reader does and fails when nothing can bring it on screen, so
// "the last Restore is clickable" is the user's question stated directly,
// rather than a claim about CSS.

const SESSIONS = Array.from({ length: 10 }, (_, i) => ({
  id: i === 0 ? SMOKE_SESSION : `2026010${i}-120000-c0ffee0${i}`,
  title: `session ${i}`,
  current: i === 0,
  model: 'gpt-5.6-luna',
  message_count: 12,
}))

// Eighteen is an ordinary archive, not a worst case: it is what a directory
// accumulates over a few weeks of work.
const ARCHIVED = Array.from({ length: 18 }, (_, i) => ({
  id: `2025120${i % 9}-120000-dead000${i % 9}`,
  title: `archived session ${i}`,
  model: 'gpt-5.6-luna',
  message_count: 8,
  bytes: 42000,
}))

test('every archived session stays reachable when the archive outgrows the drawer', async ({
  page,
}) => {
  await page.setViewportSize({ width: 900, height: 620 })

  await installMockBackend(page, {
    respond: (method) => {
      if (method === 'sessions.list') return { sessions: SESSIONS }
      if (method === 'sessions.archived') return { sessions: ARCHIVED }
      return undefined
    },
  })
  await page.goto(panelSessionURL)

  await page.locator('button.icon[title="Sessions"]').click()
  await expect(page.locator('.drawer')).toBeVisible()
  await page.locator('.drawer-archive__toggle').click()

  // Guard the fixture before asserting reachability: all 18 rows have to be
  // rendered, or "the last one is reachable" is a claim about nothing.
  const rows = page.locator('.session.archived')
  await expect(rows).toHaveCount(18)

  // Guard that the arm binds: before anything scrolls, the last row must lie
  // below the fold, or there is nothing here to fail to reach.
  //
  // Measured against the VIEWPORT rather than against the archive's scroller,
  // deliberately. That scroller is what the fix adds, so a guard phrased in
  // terms of it cannot run against the unfixed build, and the A/B would fail on
  // a missing element instead of on the thing this test is about.
  const lastRowBottom = await rows.last().evaluate((el) => el.getBoundingClientRect().bottom)
  const viewportH = await page.evaluate(() => window.innerHeight)
  expect(lastRowBottom).toBeGreaterThan(viewportH)

  // The point. The click fails if nothing can scroll this into view.
  await rows.last().locator('.drawer-archive__restore').click({ timeout: 3000 })

  // And the reason the rows scroll rather than the whole drawer: the control
  // that closes the archive again must not have scrolled away to reach them.
  await expect(page.locator('.drawer-archive__toggle')).toBeInViewport()
})
