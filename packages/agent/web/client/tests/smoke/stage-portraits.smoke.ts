import { test, expect } from '@playwright/test'
import { installMockBackend } from './support'

// Portraits off, everywhere, persistently.
//
// A browser is the only thing that can check this. The mode is CSS keyed off a
// root attribute, so happy-dom — which has no stylesheets — would report every
// portrait as present and visible whether the rule worked or not. What is being
// asserted is COMPUTED visibility, and that the tiles keep their shape.
test('stage: portraits can be turned off, and stay off', async ({ page }) => {
  await page.route('**/media/**', (route) =>
    route.fulfill({ contentType: 'image/svg+xml', body: '<svg xmlns="http://www.w3.org/2000/svg" width="80" height="80"><rect width="80" height="80" fill="#5a7a6a"/></svg>' }),
  )
  await installMockBackend(page, {
    respond: (method) => {
      if (method === 'cards.list')
        return {
          cards: [
            { id: 'elira-1', name: 'Elira', greetings: 1, avatar_url: '/media/cards/elira-1' },
            { id: 'rook-1', name: 'Rook', greetings: 1, avatar_url: '/media/cards/rook-1' },
          ],
        }
      if (method === 'personas.list') return { personas: [] }
      if (method === 'sessions.list') return { sessions: [] }
      if (method === 'worlds.list')
        return { worlds: [{ id: 'lowtown-1', name: 'Lowtown', characters: { Elira: 'elira-1' }, cover_url: '/media/worlds/lowtown-1' }] }
      return undefined
    },
  })

  await page.goto('/stage.html')
  const avatars = page.locator('.stage-card__avatar')
  await expect(avatars.first()).toBeVisible()
  const withArt = (await page.locator('.stage-card').first().boundingBox())!

  await page.locator('.stage-portraits-toggle').click()

  // Every portrait gone — the card grid AND the World shelf's cover, which is a
  // different surface with a different class and the one a per-component prop
  // would have missed.
  await expect(avatars.first()).toBeHidden()
  await expect(page.locator('.stage-world__cover')).toBeHidden()
  // ...and the names are still there. Hiding the pictures must not hide the shelf.
  await expect(page.locator('.stage-card__name', { hasText: 'Elira' })).toBeVisible()
  await expect(page.locator('.stage-world', { hasText: 'Lowtown' })).toBeVisible()

  const noArt = (await page.locator('.stage-card').first().boundingBox())!
  expect(noArt.height, 'a tile without its picture should reclaim the space, not keep it').toBeLessThan(withArt.height)

  // The name must not collide with the ☆ and ⋯ controls. They are ABSOLUTE,
  // positioned to float over the portrait — with the portrait gone they land on
  // the text, which every other assertion here passed straight through and only
  // a screenshot caught. Geometry, because "is it visible" cannot see an overlap.
  const nameBox = (await page.locator('.stage-card__name').first().boundingBox())!
  for (const ctrl of ['.stage-card__fav', '.stage-card__more']) {
    const c = (await page.locator(ctrl).first().boundingBox())!
    const overlaps =
      nameBox.x < c.x + c.width && c.x < nameBox.x + nameBox.width &&
      nameBox.y < c.y + c.height && c.y < nameBox.y + nameBox.height
    expect(overlaps, `the card name overlaps ${ctrl} with portraits off`).toBe(false)
  }
  await page.screenshot({ path: 'test-results/portraits-off.png' })

  // Persistent: a reload keeps it off, and does so BEFORE first paint (main.tsx
  // stamps the root) rather than flashing the art and then hiding it.
  await page.reload()
  await expect(page.locator('.stage-card__name', { hasText: 'Elira' })).toBeVisible()
  await expect(page.locator('.stage-card__avatar').first()).toBeHidden()

  // And reversible.
  await page.locator('.stage-portraits-toggle').click()
  await expect(page.locator('.stage-card__avatar').first()).toBeVisible()
})
