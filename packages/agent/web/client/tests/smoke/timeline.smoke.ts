import { test, expect } from '@playwright/test'
import { installMockBackend, panelSessionURL } from './support'

// Flow 2: a pinned timeline follows streaming output; once the user scrolls up
// (unpinning) it must NOT yank back to the bottom when more output arrives.
// This scroll-anchoring behavior is exactly what a component contract can't
// assert — it needs real layout and a real scroll container.
test('pinned timeline follows streaming; unpinned does not jump', async ({ page }) => {
  const backend = await installMockBackend(page)
  await page.goto(panelSessionURL)
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

  // Wait for the pacer to finish draining the burst before unpinning. A
  // teleported scrollTop=0 coalesces with any pinned auto-scroll that lands in
  // the same frame — the single scroll event then reports "at bottom" and the
  // unpin never happens. A real reader's wheel/drag fires a stream of events,
  // so only this synthetic one-shot scroll can lose that race.
  await expect(log.getByText('streaming line 39')).toBeVisible()

  // Scroll to the top → unpin. The jump button appears.
  await log.evaluate((el) => (el.scrollTop = 0))
  await expect(page.locator('.jump')).toBeVisible()

  // The unpin must survive the follow-up burst below, so make sure it landed
  // (pinnedRef reads the scroll event, which dispatches async).
  await expect.poll(() => log.evaluate((el) => el.scrollTop)).toBeLessThan(80)

  // More output arrives while unpinned…
  for (let i = 0; i < 20; i++) {
    backend.pushEvent({ type: 'text_delta', delta: `more line ${i}\n\n` })
  }

  // …and the view stays put (near the top), not yanked to the bottom.
  await expect(page.locator('.jump')).toBeVisible()
  expect(await log.evaluate((el) => el.scrollTop)).toBeLessThan(80)
})
