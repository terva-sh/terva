import { test, expect } from '@playwright/test'
import { SMOKE_SESSION, installStageBackend, stubMedia } from './support'

// User-directs (Phase 5): a play chat shows a cast strip ("who speaks?"); tapping
// an actor posts cast.speak {actor}, directing the narrator to bring them in.
// Zero backend — asserts the strip renders and sends the right verb.
test('stage: the cast strip directs who speaks', async ({ page }) => {
  await stubMedia(page)

  const CAST = { Seraphina: 'seraphina-card', Kael: 'kael' }
  let spoke: unknown = null
  const mock = await installStageBackend(page, {
    cards: [{ id: 'narrator-1', name: 'Kertoja', greetings: 1 }],
    respond: (method, params) => {
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
