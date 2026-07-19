import { test, expect } from '@playwright/test'
import { installMockBackend, SMOKE_SESSION } from './support'

// The chat composer auto-grows with its content (up to the CSS max-height cap,
// past which it scrolls) so a multiline reply is comfortable to write, then
// shrinks back once the draft is cleared. Driven against a mock just far enough
// to reach the chat view and its composer.
test('stage: the composer grows with multiline input and shrinks back', async ({ page }) => {
  await installMockBackend(page, {
    respond: (method) => {
      if (method === 'cards.list') return { cards: [{ id: 'card-1', name: 'Ivy', greetings: 1 }] }
      if (method === 'personas.list') return { personas: [] }
      if (method === 'cards.get') return { id: 'card-1', name: 'Ivy', greetings: 1, raw: {} }
      if (method === 'sessions.create') return { session: { id: SMOKE_SESSION, title: 'Chat', experience: 'chat', card: 'card-1' } }
      return undefined
    },
  })

  await page.goto('/stage.html')
  await page.locator('.stage-card').first().click()
  const ta = page.locator('.stage-composer textarea')
  await expect(ta).toBeVisible()

  const height = async () => (await ta.boundingBox())!.height

  const empty = await height()
  await ta.fill('one\ntwo\nthree\nfour\nfive')
  const grown = await height()
  expect(grown).toBeGreaterThan(empty + 20) // several lines taller than a single row

  // Clearing the draft (as a send would) collapses it back toward one row.
  await ta.fill('')
  const collapsed = await height()
  expect(collapsed).toBeLessThan(grown)
  expect(collapsed).toBeLessThanOrEqual(empty + 2) // back to ~the starting height
})
