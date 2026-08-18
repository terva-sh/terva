import { test, expect } from '@playwright/test'
import { SMOKE_SESSION, installStageBackend, stubMedia } from './support'

// Per-chat background generation (Phase 5): the steering drawer's Scene section
// grows a "describe a scene" prompt that posts backgrounds.generate {prompt};
// the daemon paints it, stores it, and binds it (tested in Go). Zero backend —
// this asserts the client sends the prompt and shows the pending state.
test('stage: generate a scene background from a prompt', async ({ page }) => {
  await stubMedia(page)

  let generated: unknown = null
  const mock = await installStageBackend(page, {
    cards: [{ id: 'c1', name: 'Iris', greetings: 1 }],
    respond: (method, params) => {
      if (method === 'sessions.create') return { session: { id: SMOKE_SESSION, title: 'Iris', experience: 'chat', card: 'c1' } }
      if (method === 'cards.get') return { id: 'c1', name: 'Iris', greetings: 1, raw: {} }
      if (method === 'backgrounds.list') return { backgrounds: [] }
      if (method === 'backgrounds.generate') {
        generated = params
        return { id: 'scene-1', url: '/media/backgrounds/scene-1' }
      }
      return undefined
    },
  })

  await page.goto('/stage.html')
  await page.locator('.stage-card').first().click()
  await expect(page.locator('.stage-composer')).toBeVisible()
  await mock.subscribed

  await page.locator('.stage-steer-btn').click()
  await expect(page.locator('.stage-drawer')).toBeVisible()
  // Since the S10 tab split, the scene generator lives behind the Scene tab.
  await page.locator('.stage-drawer__tab', { hasText: 'Scene' }).click()

  // The scene generator posts backgrounds.generate {prompt}.
  await page.locator('.stage-bg-gen__prompt').fill('a rain-slick alley at night, neon reflections')
  await expect(page.locator('.stage-bg-gen__go')).toBeEnabled()
  if (process.env.BG_SHOT) await page.screenshot({ path: `${process.env.BG_SHOT}.png`, fullPage: true })
  await page.locator('.stage-bg-gen__go').click()
  await expect.poll(() => generated).toEqual({ prompt: 'a rain-slick alley at night, neon reflections' })
})
