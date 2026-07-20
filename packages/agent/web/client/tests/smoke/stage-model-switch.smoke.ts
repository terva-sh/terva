import { test, expect } from '@playwright/test'
import { installMockBackend, SMOKE_SESSION } from './support'

// Model switching from Stage (rough-edge #1): the steering drawer's Model line is
// a live switcher. It lists the daemon's logged-in models grouped by provider
// (favorites floated up), highlights the session's current model, and switches it
// live via models.switch — the same verb the panel uses, rendered on Stage's own
// skin. Driven against a mock that plays the models.list the daemon would send.
test('stage: switch the session model from the steering drawer', async ({ page }) => {
  const MODELS = [
    { id: 'claude-opus-4-8', provider: 'anthropic', context_window: 1000000, current: true, favorite: true },
    { id: 'claude-sonnet-5', provider: 'anthropic', context_window: 1000000, favorite: true },
    { id: 'claude-haiku-4-5', provider: 'anthropic', context_window: 200000 },
    { id: 'gpt-5-codex', provider: 'openai-codex', context_window: 400000 },
  ]
  let switched: { model?: string; provider?: string } | null = null
  let faved: { model?: string; provider?: string; on?: boolean } | null = null

  const mock = await installMockBackend(page, {
    respond: (method, params) => {
      if (method === 'cards.list') return { cards: [{ id: 'card-1', name: 'Kobeni', greetings: 1 }] }
      if (method === 'personas.list') return { personas: [] }
      if (method === 'cards.get') return { id: 'card-1', name: 'Kobeni', greetings: 1, raw: {} }
      if (method === 'sessions.create') return { session: { id: SMOKE_SESSION, title: 'Kobeni', experience: 'chat', card: 'card-1' } }
      if (method === 'backgrounds.list') return { backgrounds: [] }
      if (method === 'surface.get') return { id: 'lore', title: 'Lore', kind: 'lore', lore: { entries: [] } }
      if (method === 'models.list') return { models: MODELS }
      if (method === 'models.switch') {
        switched = params as typeof switched
        return {}
      }
      if (method === 'models.favorite') {
        faved = params as typeof faved
        return {}
      }
      return undefined
    },
  })

  const snapshot = (model: string) => ({
    type: 'snapshot',
    snapshot: {
      session: { id: SMOKE_SESSION, title: 'Kobeni', experience: 'chat', card: 'card-1', provider: 'anthropic', model },
      epoch: 1,
      base: 0,
      total: 2,
      messages: [
        { role: 'user', content: [{ type: 'text', text: 'hi' }] },
        { role: 'assistant', content: [{ type: 'text', text: '"...you again."' }] },
      ],
      busy: false,
    },
  })

  await page.goto('/stage.html')
  await page.locator('.stage-card').first().click()
  await expect(page.locator('.stage-composer')).toBeVisible()
  await mock.subscribed
  mock.pushEvent(snapshot('claude-opus-4-8'), SMOKE_SESSION)
  await expect(page.locator('.stage-row--assistant')).toBeVisible()

  // Open the steering drawer; the Model line shows the session's current model.
  await page.locator('.stage-steer-btn').click()
  await expect(page.locator('.stage-modelpick__cur-id')).toHaveText('claude-opus-4-8')

  // Expand → a ★ Favorites group leads the provider groups (S10 favorites-
  // everywhere); the current model is highlighted in both homes.
  await page.locator('.stage-modelpick__current').click()
  await expect(page.locator('.stage-modelpick__list')).toBeVisible()
  await expect(page.locator('.stage-modelpick__provider')).toHaveText(['★ Favorites', 'anthropic', 'openai-codex'])
  await expect(page.locator('.stage-modelpick__row--current .stage-modelpick__id')).toHaveText(['claude-opus-4-8', 'claude-opus-4-8'])

  // Favorite toggle rides its own control without switching.
  await page.locator('.stage-modelpick__row', { hasText: 'claude-haiku-4-5' }).locator('.stage-modelpick__star').click()
  await expect.poll(() => faved).toEqual({ provider: 'anthropic', model: 'claude-haiku-4-5', on: true })
  expect(switched).toBeNull()

  // Clicking a row switches the live session (models.switch, session in frame),
  // and the list collapses.
  await page.locator('.stage-modelpick__row', { hasText: 'gpt-5-codex' }).click()
  await expect.poll(() => switched).toEqual({ model: 'gpt-5-codex', provider: 'openai-codex' })
  await expect(page.locator('.stage-modelpick__list')).toHaveCount(0)

  // The daemon's session_updated (a fresh snapshot) re-renders the current model.
  mock.pushEvent(snapshot('gpt-5-codex'), SMOKE_SESSION)
  await expect(page.locator('.stage-modelpick__cur-id')).toHaveText('gpt-5-codex')
})
