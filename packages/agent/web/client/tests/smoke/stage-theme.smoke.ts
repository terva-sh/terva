import { test, expect } from '@playwright/test'
import { installMockBackend } from './support'

// Theme presets (Phase 5): the library header's theme picker swaps the --stage-*
// palette by setting data-theme on the document root, and the choice persists
// across a reload (a client-local preference). Zero backend.
const bgVar = () => getComputedStyle(document.documentElement).getPropertyValue('--stage-bg').trim()

test('stage: theme presets re-skin and persist', async ({ page }) => {
  await installMockBackend(page, {
    respond: (method) => {
      if (method === 'cards.list') return { cards: [] }
      if (method === 'personas.list') return { personas: [] }
      return undefined
    },
  })

  await page.goto('/stage.html')
  await expect(page.locator('.stage-theme-pick')).toBeVisible()

  // Boots on the default (Dusk), and reads its palette.
  await expect(page.locator('html')).toHaveAttribute('data-theme', 'dusk')
  const dusk = await page.evaluate(bgVar)

  // Pick Nocturne → the root attribute and the palette both change.
  await page.locator('.stage-theme-pick').selectOption('nocturne')
  await expect(page.locator('html')).toHaveAttribute('data-theme', 'nocturne')
  const nocturne = await page.evaluate(bgVar)
  expect(nocturne).not.toBe(dusk)
  if (process.env.THEME_SHOT) await page.screenshot({ path: `${process.env.THEME_SHOT}.png`, fullPage: true })

  // The choice survives a reload (persisted).
  await page.reload()
  await expect(page.locator('html')).toHaveAttribute('data-theme', 'nocturne')
  expect(await page.evaluate(bgVar)).toBe(nocturne)
})
