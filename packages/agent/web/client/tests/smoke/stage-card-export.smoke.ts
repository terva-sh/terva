import { test, expect } from '@playwright/test'
import { installMockBackend, PNG_1x1_BASE64 } from './support'

// Card export (Phase 5): the library card sheet's "Export card" downloads the
// card the daemon serializes (a CCv2 PNG here). Zero backend — this asserts the
// export verb fires and the browser download lands with the right filename.
test('stage: export a card downloads a file', async ({ page }) => {
  await page.route('**/media/**', (route) =>
    route.fulfill({ contentType: 'image/svg+xml', body: '<svg xmlns="http://www.w3.org/2000/svg" width="80" height="80"><rect width="80" height="80" fill="#6a5a4a"/></svg>' }),
  )

  let exportedId = ''
  await installMockBackend(page, {
    respond: (method, params) => {
      if (method === 'cards.list') return { cards: [{ id: 'iris-1', name: 'Iris', greetings: 1, avatar_url: '/media/cards/iris-1' }] }
      if (method === 'personas.list') return { personas: [] }
      if (method === 'cards.export') {
        exportedId = (params as { id?: string })?.id ?? ''
        return { filename: 'Iris.png', mime_type: 'image/png', bytes: PNG_1x1_BASE64 }
      }
      return undefined
    },
  })

  await page.goto('/stage.html')
  await expect(page.locator('.stage-card')).toHaveCount(1)

  // Open the card's ⋯ options sheet.
  await page.locator('.stage-card__more').click()
  await expect(page.locator('.stage-sheet')).toBeVisible()

  // Export → a file download with the daemon's suggested name.
  const downloadPromise = page.waitForEvent('download')
  await page.locator('.stage-sheet__export').click()
  const download = await downloadPromise
  expect(download.suggestedFilename()).toBe('Iris.png')
  expect(exportedId).toBe('iris-1')
})
