import { test, expect } from '@playwright/test'
import { installMockBackend, SMOKE_SESSION } from './support'

// Cast/director visibility (Phase 5): a play session surfaces its cast roster in
// the steering drawer (SessionInfo.cast), and an actor_spawn tool call in the
// transcript is attributed to the actor it brought on stage (from the call's
// args) rather than shown as a raw "actor_spawn" row. Zero backend.
test('stage: play session shows its cast and attributes actor lines', async ({ page }) => {
  await page.route('**/media/**', (route) =>
    route.fulfill({ contentType: 'image/svg+xml', body: '<svg xmlns="http://www.w3.org/2000/svg" width="80" height="80"><rect width="80" height="80" fill="#5a7a6a"/></svg>' }),
  )

  const CAST = { Seraphina: 'seraphina-card', Kael: 'kael' }
  let added: unknown = null
  let removed: unknown = null
  const mock = await installMockBackend(page, {
    respond: (method, params) => {
      if (method === 'cards.list') return { cards: [{ id: 'narrator-1', name: 'Kertoja', greetings: 1 }] }
      if (method === 'personas.list') return { personas: [] }
      if (method === 'sessions.create')
        return { session: { id: SMOKE_SESSION, title: 'The Pass', experience: 'play', card: 'narrator-1', cast: CAST } }
      if (method === 'cards.get') return { id: 'narrator-1', name: 'Kertoja', greetings: 1, avatar_url: '/media/cards/narrator-1', raw: {} }
      if (method === 'backgrounds.list') return { backgrounds: [] }
      if (method === 'cast.add') {
        added = params
        return {}
      }
      if (method === 'cast.remove') {
        removed = params
        return {}
      }
      return undefined
    },
  })

  await page.goto('/stage.html')
  await page.locator('.stage-card').first().click()
  await expect(page.locator('.stage-composer')).toBeVisible()
  await mock.subscribed

  // A turn where the director (Kertoja) narrates and calls actor_spawn to bring
  // Seraphina on stage — the tool call carries the actor in its args.
  mock.pushEvent(
    {
      type: 'snapshot',
      snapshot: {
        session: { id: SMOKE_SESSION, title: 'The Pass', experience: 'play', card: 'narrator-1', cast: CAST },
        epoch: 1,
        base: 0,
        total: 3,
        messages: [
          { role: 'user', content: [{ type: 'text', text: '"Who guards the pass tonight?"' }] },
          {
            role: 'assistant',
            content: [
              { type: 'text', text: '*A figure steps from the watchtower’s shadow.*' },
              { type: 'tool_call', id: 'a1', name: 'actor_spawn', args: { actor: 'Seraphina', situation: 'A traveler asks who guards the pass.' } },
            ],
          },
          { role: 'assistant', content: [{ type: 'text', text: '*Seraphina lowers her lantern.* "I do. State your business."' }] },
        ],
        busy: false,
      },
    },
    SMOKE_SESSION,
  )

  // The actor_spawn row is attributed to Seraphina, not shown as "actor_spawn".
  await expect(page.locator('.stage-row--actor')).toHaveText('🎭 Seraphina')
  await expect(page.locator('.stage-row--tool')).toHaveCount(0)

  // The steering drawer lists the whole cast (read-only for now).
  await page.locator('.stage-steer-btn').click()
  await expect(page.locator('.stage-drawer')).toBeVisible()
  await expect(page.locator('.stage-cast__member')).toHaveCount(2)
  await expect(page.locator('.stage-cast__name').first()).toHaveText('Seraphina')
  if (process.env.CAST_SHOT) await page.screenshot({ path: `${process.env.CAST_SHOT}.png`, fullPage: true })

  // Add a new cast member — the form posts cast.add {name, ref}.
  await page.locator('.stage-cast-add__name').fill('Rook')
  await page.locator('.stage-cast-add__ref').fill('scout')
  await page.locator('.stage-cast-add__go').click()
  await expect.poll(() => added).toEqual({ name: 'Rook', ref: 'scout' })

  // Remove Seraphina — the ✕ posts cast.remove {name}.
  await page.locator('.stage-cast__member', { hasText: 'Seraphina' }).locator('.stage-cast__remove').click()
  await expect.poll(() => removed).toEqual({ name: 'Seraphina' })
})
