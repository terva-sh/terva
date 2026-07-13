import { test, expect } from '@playwright/test'
import { installMockBackend } from './support'

// Flow 2: a pinned timeline follows streaming output; once the user scrolls up
// (unpinning) it must NOT yank back to the bottom when more output arrives.
// This scroll-anchoring behavior is exactly what a component contract can't
// assert — it needs real layout and a real scroll container.
test('pinned timeline follows streaming; unpinned does not jump', async ({ page }) => {
  const backend = await installMockBackend(page)
  await page.goto('/')
  await expect(page.locator('.topbar .dot.open')).toBeVisible()
  // Wait until the client has subscribed, so pushed events aren't dropped as
  // off-session.
  await backend.subscribed

  const log = page.locator('.log')
  const distanceFromBottom = () =>
    log.evaluate((el) => el.scrollHeight - el.scrollTop - el.clientHeight)

  // Stream a tall assistant message (deltas fold into one growing item).
  for (let i = 0; i < 40; i++) {
    backend.pushEvent({ type: 'text_delta', delta: `streaming line ${i}\n\n` })
  }

  // Pinned (the default): the view rides the bottom, and there is no jump button.
  await expect.poll(distanceFromBottom, { timeout: 5000 }).toBeLessThan(80)
  await expect(page.locator('.jump')).toHaveCount(0)

  // Scroll to the top → unpin. The jump button appears.
  await log.evaluate((el) => (el.scrollTop = 0))
  await expect(page.locator('.jump')).toBeVisible()

  // More output arrives while unpinned…
  for (let i = 0; i < 20; i++) {
    backend.pushEvent({ type: 'text_delta', delta: `more line ${i}\n\n` })
  }

  // …and the view stays put (near the top), not yanked to the bottom.
  await expect(page.locator('.jump')).toBeVisible()
  expect(await log.evaluate((el) => el.scrollTop)).toBeLessThan(80)
})
