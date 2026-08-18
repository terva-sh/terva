import { test, expect } from '@playwright/test'
import { installStageBackend } from './support'

// Card URL import (Phase 5): the library's URL field posts cards.import {url};
// the daemon fetches it through the SSRF-guarded egress client. Zero backend —
// this asserts the client sends the URL and renders the card that comes back.
test('stage: import a card from a URL', async ({ page }) => {
  let importedUrl = ''
  await installStageBackend(page, {
    respond: (method, params) => {
      if (method === 'cards.list')
        return { cards: importedUrl ? [{ id: 'remote-1', name: 'Remote', greetings: 1 }] : [] }
      if (method === 'cards.import') {
        importedUrl = (params as { url?: string })?.url ?? ''
        return { id: 'remote-1', name: 'Remote', greetings: 1, raw: {} }
      }
      return undefined
    },
  })

  await page.goto('/stage.html')
  await expect(page.locator('.stage-import-url__input')).toBeVisible()
  // Empty library, and the import button stays disabled until a URL is typed.
  await expect(page.locator('.stage-card')).toHaveCount(0)
  await expect(page.locator('.stage-import-url__go')).toBeDisabled()

  await page.locator('.stage-import-url__input').fill('https://chub.ai/characters/anon/remote')
  await expect(page.locator('.stage-import-url__go')).toBeEnabled()
  await page.locator('.stage-import-url__go').click()

  // The URL reached the wire, and the card it returned renders after the reload.
  await expect(page.locator('.stage-card')).toHaveCount(1)
  await expect(page.locator('.stage-card__name')).toHaveText('Remote')
  expect(importedUrl).toBe('https://chub.ai/characters/anon/remote')
  if (process.env.STAGE_URL_SHOT) await page.screenshot({ path: `${process.env.STAGE_URL_SHOT}.png`, fullPage: true })
})
