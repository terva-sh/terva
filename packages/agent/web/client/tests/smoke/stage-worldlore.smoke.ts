import { test, expect } from '@playwright/test'
import { installMockBackend, SMOKE_SESSION } from './support'

// The World tab (Worlds W2): a chat session's steering drawer grows a World
// tab — the roster kept on stage (W2a) plus the editable World lore (L1).
// Entries render from SessionInfo.world_lore; add/edit/delete post
// world.lore.put / world.lore.delete and the list re-renders from the
// snapshot. Zero backend.
test('stage: the World tab lists the roster and edits World lore', async ({ page }) => {
  await page.route('**/media/**', (route) =>
    route.fulfill({ contentType: 'image/svg+xml', body: '<svg xmlns="http://www.w3.org/2000/svg" width="80" height="80"><rect width="80" height="80" fill="#5a7a6a"/></svg>' }),
  )

  const LORE = [
    { name: 'The Accord', constant: true, content: 'Magic is outlawed in the city.' },
    { name: 'Guild debt', keys: ['debt', 'guild'], content: 'Elira owes the guild a life.', audience: ['Elira'], model: true, learned: { Elira: '2026-07-19T00:00:00Z' } },
  ]
  const SESSION = {
    id: SMOKE_SESSION,
    title: 'The Lowtown Job',
    experience: 'chat',
    card: 'elira-1',
    cast: { Rook: 'rook-1' },
    world_lore: LORE,
  }
  const puts: unknown[] = []
  let deleted: unknown = null
  let removed: unknown = null
  const mock = await installMockBackend(page, {
    respond: (method, params) => {
      if (method === 'cards.list') return { cards: [{ id: 'elira-1', name: 'Elira', greetings: 1 }] }
      if (method === 'personas.list') return { personas: [] }
      if (method === 'sessions.create') return { session: SESSION }
      if (method === 'cards.get') return { id: 'elira-1', name: 'Elira', greetings: 1, avatar_url: '/media/cards/elira-1', raw: {} }
      if (method === 'backgrounds.list') return { backgrounds: [] }
      if (method === 'world.lore.put') {
        puts.push(params)
        return {}
      }
      if (method === 'world.lore.delete') {
        deleted = params
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

  // Seed the drawer's SessionInfo via the snapshot fold.
  mock.pushEvent(
    { type: 'snapshot', snapshot: { session: SESSION, epoch: 1, base: 0, total: 0, messages: [], busy: false } },
    SMOKE_SESSION,
  )

  // Open the drawer and land on the World tab (the reserved left flank).
  await page.locator('.stage-steer-btn').click()
  await expect(page.locator('.stage-drawer')).toBeVisible()
  await page.locator('.stage-drawer__tab', { hasText: 'World' }).click()

  // On stage (W2a + W4a): the bound character leads (main character row, not
  // removable), then the kept-on-stage roster (removable).
  await expect(page.locator('.stage-cast__member')).toHaveCount(2)
  await expect(page.locator('.stage-cast__member').first()).toContainText('main character')
  await expect(page.locator('.stage-cast__remove')).toHaveCount(1)

  // The World lore list renders both entries: the constant one badged
  // "always on", the keyed one with its trigger chips.
  await expect(page.locator('.stage-worldlore__entry')).toHaveCount(2)
  const accord = page.locator('.stage-worldlore__entry', { hasText: 'The Accord' })
  await expect(accord.locator('.stage-lore__const')).toHaveText('always on')
  await expect(accord.locator('.stage-lore__content')).toHaveText('Magic is outlawed in the city.')
  const debt = page.locator('.stage-worldlore__entry', { hasText: 'Guild debt' })
  await expect(debt.locator('.stage-lore__key')).toHaveCount(2)
  // A targeted entry (L2) is badged with who knows it; a model-noted one
  // (world_note, W4b) carries 📝 and the ledger rides the tooltip.
  await expect(debt.locator('.stage-worldlore__audience')).toHaveText('🔒 Elira')
  await expect(debt.locator('.stage-worldlore__audience')).toHaveAttribute('title', /learned mid-scene/)
  await expect(debt.locator('.stage-worldlore__model')).toBeVisible()

  // Add a keyed entry — the form posts world.lore.put {entry} (no replace).
  await page.locator('.stage-worldlore-form__name').fill('The Watch')
  await page.locator('.stage-worldlore-form__keys').fill('watch, patrol')
  await page.locator('.stage-worldlore-form__content').fill('The night watch answers to the guild.')
  await page.locator('.stage-worldlore-form__save').click()
  await expect.poll(() => puts.length).toBe(1)
  expect(puts[0]).toEqual({
    entry: { name: 'The Watch', keys: ['watch', 'patrol'], constant: false, content: 'The night watch answers to the guild.' },
  })
  // The form clears on success (the list itself re-renders from the snapshot).
  await expect(page.locator('.stage-worldlore-form__name')).toHaveValue('')

  // The snapshot fold re-renders the list.
  mock.pushEvent(
    {
      type: 'snapshot',
      snapshot: {
        session: { ...SESSION, world_lore: [...LORE, { name: 'The Watch', keys: ['watch', 'patrol'], content: 'The night watch answers to the guild.' }] },
        epoch: 1,
        base: 0,
        total: 0,
        messages: [],
        busy: false,
      },
    },
    SMOKE_SESSION,
  )
  await expect(page.locator('.stage-worldlore__entry')).toHaveCount(3)

  // Edit an entry: ✎ pre-fills the form; saving posts put with replace, so a
  // rename edits in place.
  await accord.locator('.stage-worldlore__act[title="Edit The Accord"]').click()
  await expect(page.locator('.stage-worldlore-form__name')).toHaveValue('The Accord')
  // An always-on entry hides the keyword input.
  await expect(page.locator('.stage-worldlore-form__keys')).toHaveCount(0)
  await page.locator('.stage-worldlore-form__content').fill('The Accord has fallen.')
  await page.locator('.stage-worldlore-form__save').click()
  await expect.poll(() => puts.length).toBe(2)
  expect(puts[1]).toEqual({
    entry: { name: 'The Accord', constant: true, content: 'The Accord has fallen.' },
    replace: 'The Accord',
  })

  // A secret: toggling an audience chip scopes the entry (L2) — the put
  // carries who knows it.
  await page.locator('.stage-worldlore-form__name').fill('The Betrayal')
  await page.locator('.stage-worldlore-form__keys').fill('betrayal')
  await page.locator('.stage-worldlore-form__content').fill('The guildmaster sold them out.')
  await page.locator('.stage-worldlore-form__chip', { hasText: 'Elira' }).click()
  await page.locator('.stage-worldlore-form__save').click()
  await expect.poll(() => puts.length).toBe(3)
  expect(puts[2]).toEqual({
    entry: { name: 'The Betrayal', keys: ['betrayal'], constant: false, content: 'The guildmaster sold them out.', audience: ['Elira'] },
  })

  // Delete posts world.lore.delete {name}.
  await page.locator('.stage-worldlore__act[title="Delete Guild debt"]').click()
  await expect.poll(() => deleted).toEqual({ name: 'Guild debt' })

  // Removing a roster member posts cast.remove {name} — same verb as play.
  await page.locator('.stage-cast__remove').click()
  await expect.poll(() => removed).toEqual({ name: 'Rook' })
})
