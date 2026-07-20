import { test, expect } from '@playwright/test'
import { installMockBackend, SMOKE_SESSION } from './support'

// Model PROVENANCE in the Stage picker. The ★ Favorites section is a flat,
// cross-provider list with no provider heading of its own — so when the same
// model id is reachable through two backends (a subscription plan and a metered
// key, the case that motivated this), both favorites rendered as the same bare
// string and choosing the cheaper one was a coin flip.
//
// Each favorite now carries its provider and how that provider bills; the group
// headings carry the billing chip for their rows; and the collapsed button names
// the provider of the model you are actually on.
const MODELS = [
  // The collision: one id, two backends, opposite billing.
  { id: 'deep-v4-pro', provider: 'anthropic', context_window: 200000, auth: 'oauth', favorite: true, current: true },
  { id: 'deep-v4-pro', provider: 'deepseek', context_window: 200000, auth: 'apikey', favorite: true },
  // A keyless backend: no credential, so no honest billing answer to give.
  { id: 'local-qwen', provider: 'workshop', context_window: 262144, favorite: true },
  { id: 'claude-haiku-4-5', provider: 'anthropic', context_window: 200000, auth: 'oauth' },
]

const snapshot = {
  type: 'snapshot',
  snapshot: {
    session: { id: SMOKE_SESSION, title: 'Kobeni', experience: 'chat', card: 'card-1', provider: 'anthropic', model: 'deep-v4-pro' },
    epoch: 1,
    base: 0,
    total: 2,
    messages: [
      { role: 'user', content: [{ type: 'text', text: 'hi' }] },
      { role: 'assistant', content: [{ type: 'text', text: '"...you again."' }] },
    ],
    busy: false,
  },
}

async function openPicker(page: import('@playwright/test').Page) {
  const mock = await installMockBackend(page, {
    respond: (method) => {
      if (method === 'cards.list') return { cards: [{ id: 'card-1', name: 'Kobeni', greetings: 1 }] }
      if (method === 'personas.list') return { personas: [] }
      if (method === 'cards.get') return { id: 'card-1', name: 'Kobeni', greetings: 1, raw: {} }
      if (method === 'sessions.create') return { session: { id: SMOKE_SESSION, title: 'Kobeni', experience: 'chat', card: 'card-1' } }
      if (method === 'backgrounds.list') return { backgrounds: [] }
      if (method === 'surface.get') return { id: 'lore', title: 'Lore', kind: 'lore', lore: { entries: [] } }
      if (method === 'models.list') return { models: MODELS }
      return undefined
    },
  })
  await page.goto('/stage.html')
  await page.locator('.stage-card').first().click()
  await expect(page.locator('.stage-composer')).toBeVisible()
  await mock.subscribed
  mock.pushEvent(snapshot, SMOKE_SESSION)
  await expect(page.locator('.stage-row--assistant')).toBeVisible()
  await page.locator('.stage-steer-btn').click()
  await page.locator('.stage-modelpick__current').click()
  await expect(page.locator('.stage-modelpick__list')).toBeVisible()
  return mock
}

test('stage: two favorites sharing a model id are told apart by provider and billing', async ({ page }) => {
  await openPicker(page)

  const favGroup = page.locator('.stage-modelpick__group').first()
  await expect(favGroup.locator('.stage-modelpick__provider')).toHaveText('★ Favorites')

  // Both collided rows are present...
  const collided = favGroup.locator('.stage-modelpick__row', { hasText: 'deep-v4-pro' })
  await expect(collided).toHaveCount(2)
  // ...and they are NO LONGER identical: each names its own backend.
  await expect(collided.locator('.stage-modelpick__prov')).toHaveText(['anthropic', 'deepseek'])
  // The whole point: which one spends the subscription and which one bills per token.
  await expect(collided.locator('.stage-modelpick__auth')).toHaveText(['sub', 'api'])

  // A keyless backend reports no method, so it gets NO badge rather than a guess —
  // it still shows its provider, which is what disambiguates it.
  const keyless = favGroup.locator('.stage-modelpick__row', { hasText: 'local-qwen' })
  await expect(keyless.locator('.stage-modelpick__prov')).toHaveText('workshop')
  await expect(keyless.locator('.stage-modelpick__auth')).toHaveCount(0)
})

test('stage: provider groups carry the billing chip, and the collapsed button names the provider', async ({ page }) => {
  await openPicker(page)

  // The heading carries the chip once for its rows rather than repeating it on
  // each — inside a group the provider is never ambiguous to begin with.
  const anthropic = page.locator('.stage-modelpick__group', { hasText: 'claude-haiku-4-5' })
  await expect(anthropic.locator('.stage-modelpick__provider .stage-modelpick__auth')).toHaveText('sub')
  await expect(anthropic.locator('.stage-modelpick__row', { hasText: 'claude-haiku-4-5' }).locator('.stage-modelpick__prov')).toHaveCount(0)

  // The collapsed button had the same ambiguity as a favorites row: a bare id does
  // not say which of the two backends this session is actually about to bill.
  await page.locator('.stage-modelpick__current').click()
  await expect(page.locator('.stage-modelpick__list')).toHaveCount(0)
  await expect(page.locator('.stage-modelpick__cur-id')).toHaveText('deep-v4-pro')
  await expect(page.locator('.stage-modelpick__cur-prov')).toHaveText('anthropic')
})
