import { test, expect } from '@playwright/test'
import { installMockBackend, panelSessionURL } from './support'

// Flow: the live thinking block. It rides its own event (reasoning_delta), is
// held outside `items`, and is dropped when the turn ends — so a real browser is
// the only place to confirm it renders where intended and that its height stays
// bounded when a provider sends prose instead of a headline.
//
// 🪤 This test used to assert the OPPOSITE of two things it now asserts, and the
// old rules were not arbitrary. The display was a single clipped row: sections
// superseded each other, and the row stayed exactly one line tall, because a
// second line would have pushed the composer off screen. The block accumulates
// sections instead — a thought that vanished the moment the model moved on was
// one nobody got to read — and a CSS cap now buys what the truncation used to.
// The invariant that survives both designs is the last one here: the composer
// stays on screen.
test('live thinking accumulates in a capped block and clears with the turn', async ({ page }) => {
  const backend = await installMockBackend(page)
  await page.goto(panelSessionURL)
  await expect(page.locator('.topbar .dot.open')).toBeVisible()
  await backend.subscribed

  backend.pushEvent({ type: 'turn_start', step: 1 })
  backend.pushEvent({ type: 'assistant_start' })
  backend.pushEvent({ type: 'reasoning_delta', delta: '**Inspecting commit before push**' })

  const block = page.locator('.reasoning--live')
  const body = page.locator('.reasoning-summary--live')
  await expect(block).toBeVisible()
  await expect(body).toContainText('Inspecting commit before push')
  // The provider's markup does not reach the screen: markdown renders it.
  await expect(body).not.toContainText('**')

  // A later section JOINS the earlier one rather than replacing it.
  backend.pushEvent({ type: 'reasoning_delta', delta: '\n\n**Editing the handler**' })
  await expect(body).toContainText('Editing the handler')
  await expect(body).toContainText('Inspecting commit before push')

  // Prose, not a headline. The block must not grow without bound: it caps and
  // scrolls instead.
  backend.pushEvent({
    type: 'reasoning_delta',
    delta: '\n\n' + 'Let me analyze the results carefully and at considerable length. '.repeat(30),
  })
  await expect(body).toContainText('Let me analyze')

  const box = await body.evaluate((el) => ({
    client: el.clientHeight,
    scroll: el.scrollHeight,
    top: el.scrollTop,
  }))
  expect(box.scroll).toBeGreaterThan(box.client) // capped, so the content overflows
  // Pinned to the tail: the newest thought is what is on screen, not the oldest.
  expect(box.top + box.client).toBeGreaterThanOrEqual(box.scroll - 4)

  // The invariant the one-line row existed to protect, and the one that must
  // hold whatever shape the display takes: thinking never costs you the input.
  const composer = page.locator('footer.composer textarea')
  await expect(composer).toBeVisible()
  const onScreen = await composer.evaluate((el) => {
    const r = el.getBoundingClientRect()
    return r.top >= 0 && r.bottom <= window.innerHeight + 1
  })
  expect(onScreen).toBe(true)

  // Gone when the turn is.
  backend.pushEvent({ type: 'turn_end' })
  await expect(block).toHaveCount(0)
})
