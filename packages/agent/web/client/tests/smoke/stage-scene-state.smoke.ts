import { test, expect } from '@playwright/test'
import { installMockBackend, SMOKE_SESSION } from './support'

// The pinned scene-state card (SD4): a reserved World-lore entry renders as a
// card above the composer (not in the drawer's lore list), collapsed to its
// first line; open it to edit (world.lore.put) or unpin (world.lore.delete);
// and the doctor's scene_state proposal applies through the same put. Zero
// backend.
test('stage: the scene-state card pins, edits, unpins, and takes doctor updates', async ({ page }) => {
  await page.route('**/media/**', (route) =>
    route.fulfill({ contentType: 'image/svg+xml', body: '<svg xmlns="http://www.w3.org/2000/svg" width="80" height="80"><rect width="80" height="80" fill="#5a7a6a"/></svg>' }),
  )

  const PIN = { name: 'Scene state', constant: true, content: 'Day 14, first light.\n3 silver owed to Marrow.', model: true }
  const SESSION = {
    id: SMOKE_SESSION,
    title: 'The Lowtown Job',
    experience: 'chat',
    card: 'elira-1',
    world_lore: [PIN, { name: 'The bell', keys: ['bell'], content: 'Rings at dusk.' }],
  }
  const puts: Record<string, unknown>[] = []
  const deletes: Record<string, unknown>[] = []
  const mock = await installMockBackend(page, {
    respond: (method, params) => {
      if (method === 'cards.list') return { cards: [{ id: 'elira-1', name: 'Elira', greetings: 1 }] }
      if (method === 'personas.list') return { personas: [] }
      if (method === 'backgrounds.list') return { backgrounds: [] }
      if (method === 'sessions.create') return { session: SESSION }
      if (method === 'cards.get') return { id: 'elira-1', name: 'Elira', greetings: 1, avatar_url: '/media/cards/elira-1', raw: {} }
      if (method === 'sessions.doctor') {
        return {
          proposals: [
            { id: 'p1', kind: 'scene_state', rationale: 'the clock moved past the card', name: 'Scene state', content: 'Day 15, dusk. The north road.' },
          ],
        }
      }
      if (method === 'world.lore.put') {
        puts.push(params as Record<string, unknown>)
        return {}
      }
      if (method === 'world.lore.delete') {
        deletes.push(params as Record<string, unknown>)
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
    { type: 'snapshot', snapshot: { session: SESSION, epoch: 1, base: 0, total: 0, messages: [], busy: false } },
    SMOKE_SESSION,
  )

  // Collapsed: one line above the composer — the card's first line only.
  const strip = page.locator('.stage-scene-state--closed')
  await expect(strip).toBeVisible()
  await expect(strip).toContainText('Day 14, first light.')
  await expect(strip).not.toContainText('3 silver')

  // The drawer's lore list carries the ordinary entry but NOT the pin (it
  // lives above the composer), and no pin-offer button while a pin exists.
  await page.locator('.stage-steer-btn').click()
  await page.locator('.stage-drawer__tab', { hasText: 'World' }).click()
  await expect(page.locator('.stage-worldlore__entry', { hasText: 'The bell' })).toBeVisible()
  await expect(page.locator('.stage-worldlore__entry', { hasText: 'Scene state' })).toHaveCount(0)
  await expect(page.locator('.stage-worldlore__pinbtn')).toHaveCount(0)

  // The doctor's scene_state kind: pinned label, no shared/audience scope,
  // accept applies through the same world.lore.put the card's editor uses.
  await page.locator('.stage-doctor__run').click()
  const proposal = page.locator('.stage-doctor__item')
  await expect(proposal).toHaveCount(1)
  await expect(proposal.locator('.stage-doctor__kind')).toContainText('scene state')
  await expect(proposal.locator('.stage-doctor__scope')).toContainText('replaces the pinned card')
  await proposal.locator('.stage-doctor__accept').click()
  await expect(proposal).toContainText('✓ applied')
  expect(puts[0]).toMatchObject({ entry: { name: 'Scene state', constant: true, content: 'Day 15, dusk. The north road.' } })
  await page.locator('.stage-drawer__close').click()

  // Open the card, edit, save: the put carries the canonical pin shape.
  await strip.click()
  const card = page.locator('.stage-scene-state--open')
  await expect(card).toBeVisible()
  await expect(card.locator('.stage-scene-state__content')).toContainText('3 silver owed to Marrow.')
  await expect(card.locator('.stage-scene-state__model')).toBeVisible()
  await card.locator('button', { hasText: 'Edit' }).click()
  await card.locator('.stage-scene-state__editor').fill('Day 15, dusk. Debt cleared.')
  await card.locator('.stage-scene-state__save').click()
  await expect(card.locator('.stage-scene-state__content')).toBeVisible() // back to view mode
  expect(puts[1]).toMatchObject({ entry: { name: 'Scene state', constant: true, content: 'Day 15, dusk. Debt cleared.' } })

  // Unpin (confirmed) → world.lore.delete on the pin's name.
  page.once('dialog', (d) => void d.accept())
  await card.locator('.stage-scene-state__unpin').click()
  await expect.poll(() => deletes.length).toBe(1)
  expect(deletes[0]).toMatchObject({ name: 'Scene state' })
})

// No pin yet: the World tab offers to start one, prefilling the lore form
// with the reserved name and always-on — the server normalizes the rest.
test('stage: the World tab offers the scene-state pin when nothing is pinned', async ({ page }) => {
  await page.route('**/media/**', (route) =>
    route.fulfill({ contentType: 'image/svg+xml', body: '<svg xmlns="http://www.w3.org/2000/svg" width="80" height="80"><rect width="80" height="80" fill="#5a7a6a"/></svg>' }),
  )
  const SESSION = { id: SMOKE_SESSION, title: 'Fresh', experience: 'chat', card: 'elira-1', world_lore: [] }
  const mock = await installMockBackend(page, {
    respond: (method) => {
      if (method === 'cards.list') return { cards: [{ id: 'elira-1', name: 'Elira', greetings: 1 }] }
      if (method === 'personas.list') return { personas: [] }
      if (method === 'backgrounds.list') return { backgrounds: [] }
      if (method === 'sessions.create') return { session: SESSION }
      if (method === 'cards.get') return { id: 'elira-1', name: 'Elira', greetings: 1, avatar_url: '/media/cards/elira-1', raw: {} }
      return undefined
    },
  })

  await page.goto('/stage.html')
  await page.locator('.stage-card').first().click()
  await expect(page.locator('.stage-composer')).toBeVisible()
  await mock.subscribed
  mock.pushEvent(
    { type: 'snapshot', snapshot: { session: SESSION, epoch: 1, base: 0, total: 0, messages: [], busy: false } },
    SMOKE_SESSION,
  )

  // No pin → no card above the composer, and the drawer offers one.
  await expect(page.locator('.stage-scene-state')).toHaveCount(0)
  await page.locator('.stage-steer-btn').click()
  await page.locator('.stage-drawer__tab', { hasText: 'World' }).click()
  await page.locator('.stage-worldlore__pinbtn').click()
  await expect(page.locator('.stage-worldlore-form__name')).toHaveValue('Scene state')
  await expect(page.locator('.stage-worldlore-form__always input')).toBeChecked()
})
