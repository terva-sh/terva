import { test, expect } from '@playwright/test'
import { installMockBackend } from './support'

// Flow 5: overlays close through both the keyboard (Escape) and the backdrop
// (scrim click) paths. The model picker is the representative modal — it has an
// autofocused search input with an Escape handler and a click-to-dismiss scrim.
test('model picker closes via Escape and via backdrop', async ({ page }) => {
  await installMockBackend(page)
  await page.goto('/')

  const openBtn = page.locator('button.model-btn')
  const scrim = page.locator('.modal-scrim')
  const search = page.locator('input.pick-search')

  // Escape path — the search input is autofocused and handles Escape → close.
  await openBtn.click()
  await expect(scrim).toBeVisible()
  await expect(search).toBeFocused()
  await search.press('Escape')
  await expect(scrim).toHaveCount(0)

  // Backdrop path — clicking the scrim (away from the inner .modal, which stops
  // propagation) closes it.
  await openBtn.click()
  await expect(scrim).toBeVisible()
  await scrim.click({ position: { x: 5, y: 5 } })
  await expect(scrim).toHaveCount(0)
})
