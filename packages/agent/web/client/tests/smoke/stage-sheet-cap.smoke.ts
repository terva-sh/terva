import { test, expect } from '@playwright/test'
import { installStageBackend } from './support'

// A Stage sheet must not grow off the top of the screen.
//
// .stage-sheet-backdrop is align-items:flex-end, so an oversized sheet does not
// clip at the bottom the way a centred modal does. It grows UPWARD, and the
// first thing to leave is the header: the group's name field and the button
// that closes the sheet. Nothing scrolls it back, because the backdrop is fixed
// and neither it nor the sheet had an overflow that scrolls.
//
// The group sheet is the surface this bites, because it was the only sheet on
// the bare .stage-sheet: --chats and --detail had each been given a cap for
// their own content while the base stayed unguarded. Measured before the fix at
// 740x360 with a 30-card group, the sheet ran -35..360 and the name field
// -19..17, so more than half of it sat above the fold.
//
// Landscape phone, not a contrived height: Stage is a phone-first surface and
// 360 is an ordinary landscape viewport.

const CARDS = Array.from({ length: 30 }, (_, i) => ({
  id: `card-${i}`,
  name: `Character ${i}`,
  greetings: 1,
}))

const GROUPS = [{ id: 'g1', name: 'Everyone', color: '#8ab', members: CARDS.map((c) => c.id) }]

test('stage: a full group sheet stays on screen in landscape', async ({ page }) => {
  await page.setViewportSize({ width: 740, height: 360 })

  await installStageBackend(page, {
    cards: CARDS,
    respond: (method) => (method === 'cardgroups.list' ? { groups: GROUPS } : undefined),
  })
  await page.goto('/stage.html')
  await expect(page.locator('.stage-card').first()).toBeVisible()

  await page.locator('.stage-groupchip__manage').first().click()
  const sheet = page.locator('.stage-groupsheet')
  await expect(sheet).toBeVisible()

  // Guard the fixture: the sheet has to be carrying the whole group, or it was
  // never big enough to escape and this test proves nothing.
  await expect(sheet.locator('.stage-groupsheet__card')).toHaveCount(30)

  // Guard that the arm binds. Phrased on the card list, which exists and
  // overflows in BOTH states, so it still runs against the unfixed build
  // instead of failing on something the fix introduced.
  const listRoom = await sheet
    .locator('.stage-groupsheet__cards')
    .evaluate((el) => el.scrollHeight - el.clientHeight)
  expect(listRoom, 'the group must overflow its card list, or the sheet was never stressed').toBeGreaterThan(200)

  // The point. The sheet's top edge is on screen, so its header came with it.
  const sheetBox = await sheet.boundingBox()
  expect(sheetBox, 'the sheet must have a box').not.toBeNull()
  expect(sheetBox!.y, 'the sheet must not grow off the top of the screen').toBeGreaterThanOrEqual(0)

  // …and the consequence a user meets: the name field is whole, not a sliver
  // bisected by the top edge. Asserted on the box rather than with
  // toBeInViewport, which reports a partially clipped element as in-viewport.
  const nameBox = await sheet.locator('.stage-groupsheet__name').boundingBox()
  expect(nameBox, 'the name field must have a box').not.toBeNull()
  expect(nameBox!.y, 'the name field must be fully on screen').toBeGreaterThanOrEqual(0)

  // It is still usable, which is the whole reason the header matters.
  await sheet.locator('.stage-groupsheet__name').fill('renamed', { timeout: 3000 })
  await expect(sheet.locator('.stage-groupsheet__name')).toHaveValue('renamed')
})
