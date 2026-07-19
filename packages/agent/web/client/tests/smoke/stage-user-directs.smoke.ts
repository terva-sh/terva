import { test, expect } from '@playwright/test'
import { installMockBackend, SMOKE_SESSION } from './support'

// User-directs (Phase 5): a play chat shows a cast strip ("who speaks?"); tapping
// an actor posts cast.speak {actor}, directing the narrator to bring them in.
// Zero backend — asserts the strip renders and sends the right verb.
test('stage: the cast strip directs who speaks', async ({ page }) => {
  await page.route('**/media/**', (route) =>
    route.fulfill({ contentType: 'image/svg+xml', body: '<svg xmlns="http://www.w3.org/2000/svg" width="80" height="80"><rect width="80" height="80" fill="#4a5a6a"/></svg>' }),
  )

  const CAST = { Seraphina: 'seraphina-card', Kael: 'kael' }
  let spoke: unknown = null
  const mock = await installMockBackend(page, {
    respond: (method, params) => {
      if (method === 'cards.list') return { cards: [{ id: 'narrator-1', name: 'Kertoja', greetings: 1 }] }
      if (method === 'personas.list') return { personas: [] }
      if (method === 'sessions.create') return { session: { id: SMOKE_SESSION, title: 'The Pass', experience: 'play', card: 'narrator-1', cast: CAST } }
      if (method === 'cards.get') return { id: 'narrator-1', name: 'Kertoja', greetings: 1, raw: {} }
      if (method === 'cast.speak') {
        spoke = params
        return {}
      }
      return undefined
    },
  })

  await page.goto('/stage.html')
  await page.locator('.stage-card').first().click()
  await expect(page.locator('.stage-composer')).toBeVisible()
  await mock.subscribed
  mock.pushEvent(
    {
      type: 'snapshot',
      snapshot: {
        session: { id: SMOKE_SESSION, title: 'The Pass', experience: 'play', card: 'narrator-1', cast: CAST },
        epoch: 1,
        base: 0,
        total: 1,
        messages: [{ role: 'assistant', content: [{ type: 'text', text: '*The pass is quiet.*' }] }],
        busy: false,
      },
    },
    SMOKE_SESSION,
  )

  // The cast strip lists the ensemble; tapping one directs the narrator.
  await expect(page.locator('.stage-cast-strip__actor')).toHaveCount(2)
  await page.locator('.stage-cast-strip__actor', { hasText: 'Seraphina' }).click()
  await expect.poll(() => spoke).toEqual({ actor: 'Seraphina' })
  if (process.env.DIRECTS_SHOT) await page.screenshot({ path: `${process.env.DIRECTS_SHOT}.png`, fullPage: true })
})
