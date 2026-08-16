import { test, expect } from '@playwright/test'
import { installMockBackend, panelSessionURL } from './support'

// Flow: the live reasoning row. It rides its own event (reasoning_delta), is
// held outside `items`, and is dropped when the turn ends — so a real browser is
// the only place to confirm it renders where intended and clips rather than
// wraps when a provider sends prose instead of a headline.
test('live reasoning renders on its own row and clears with the turn', async ({ page }) => {
  const backend = await installMockBackend(page)
  await page.goto(panelSessionURL)
  await expect(page.locator('.topbar .dot.open')).toBeVisible()
  await backend.subscribed

  backend.pushEvent({ type: 'turn_start', step: 1 })
  backend.pushEvent({ type: 'assistant_start' })
  backend.pushEvent({ type: 'reasoning_delta', delta: '**Inspecting commit before push**' })

  const row = page.locator('.reasoning-line')
  await expect(row).toBeVisible()
  // The provider's markup does not reach the screen.
  await expect(row).toHaveText('Inspecting commit before push')

  // A later section supersedes the earlier one rather than stacking.
  backend.pushEvent({ type: 'reasoning_delta', delta: '\n\n**Editing the handler**' })
  await expect(row).toHaveText('Editing the handler')

  // Prose, not a headline: the row must stay ONE line tall, or it pushes the
  // composer off screen. Measured against a single-line height, not a constant.
  const oneLine = await row.evaluate((el) => el.getBoundingClientRect().height)
  backend.pushEvent({
    type: 'reasoning_delta',
    delta: '\n\n' + 'Let me analyze the results carefully and at considerable length. '.repeat(30),
  })
  await expect(row).toContainText('Let me analyze')
  const proseHeight = await row.evaluate((el) => el.getBoundingClientRect().height)
  expect(proseHeight).toBe(oneLine)

  // Gone when the turn is.
  backend.pushEvent({ type: 'turn_end' })
  await expect(row).toHaveCount(0)
})
