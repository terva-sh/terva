import { test, expect } from '@playwright/test'
import { SMOKE_SESSION, installStageBackend } from './support'

// Directed vs routed authorship, side by side — the case from the 2026-07-19
// dogfooding review. At L101 the user posted a narrator beat they had written
// themselves; at L102 the meta-narrator invented one and the model wrote it.
// Consecutive rows, both rendered as an unadorned "🎭 Narrator", nothing on
// screen saying which was whose. The wire has always distinguished them
// (WireMessage.Directed vs .Routed); only the UI collapsed the two.
test('stage: a line you wrote and a line the router invented are told apart', async ({ page }) => {
  const mock = await installStageBackend(page, {
    cards: [{ id: 'card-1', name: 'Kobeni', greetings: 1 }],
    respond: (method) => {
      if (method === 'cards.get') return { id: 'card-1', name: 'Kobeni', greetings: 1, raw: {} }
      if (method === 'sessions.create') return { session: { id: SMOKE_SESSION, title: 'Kobeni', experience: 'chat', card: 'card-1' } }
      if (method === 'backgrounds.list') return { backgrounds: [] }
      if (method === 'surface.get') return { id: 'lore', title: 'Lore', kind: 'lore', lore: { entries: [] } }
      if (method === 'models.list') return { models: [] }
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
        session: { id: SMOKE_SESSION, title: 'Kobeni', experience: 'chat', card: 'card-1' },
        epoch: 1,
        base: 0,
        total: 4,
        messages: [
          { role: 'user', content: [{ type: 'text', text: 'I knock.' }] },
          // The user's own narrator beat.
          { role: 'assistant', content: [{ type: 'text', text: '*The bathhouse door swings shut behind Kobeni.*' }], directed: true },
          // The router's narrator beat.
          { role: 'assistant', content: [{ type: 'text', text: '*The western bell tower marks the hour.*' }], routed: true },
          // A routed line attributed to a named character.
          { role: 'assistant', content: [{ type: 'text', text: '"You came back."' }], routed: true, actor: 'Elira' },
        ],
        busy: false,
      },
    },
    SMOKE_SESSION,
  )

  // One of each class — not three of the same.
  await expect(page.locator('.stage-row--directed')).toHaveCount(1)
  await expect(page.locator('.stage-row--routed')).toHaveCount(2)

  // The two consecutive narrator beats read differently: yours is signed, the
  // router's wears the mask.
  await expect(page.locator('.stage-row--directed .stage-row__name')).toHaveText('✍ Narrator')
  await expect(page.locator('.stage-row--routed .stage-row__name').first()).toHaveText('🎭 Narrator')
  await expect(page.locator('.stage-row--routed .stage-row__name').nth(1)).toHaveText('🎭 Elira')

  // Hovering says which is which in words, not just glyphs.
  await expect(page.locator('.stage-row--directed .stage-row__name')).toHaveAttribute('title', /you wrote this line/i)
  await expect(page.locator('.stage-row--routed .stage-row__name').first()).toHaveAttribute('title', /meta-narrator/i)
})
