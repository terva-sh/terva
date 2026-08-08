import { test, expect } from '@playwright/test'
import { installMockBackend, SMOKE_SESSION } from './support'

// Saved Worlds (W5): the Library grows a Worlds shelf; a World sheet lists its
// characters and chats and starts a new chat INSIDE the World
// (sessions.create {world}); the steering World tab promotes an unsaved World
// (worlds.save {name}) and saves changes back to a saved one (worlds.save {}).
// Zero backend.
test('stage: the Worlds shelf, chat-in-World, and promotion', async ({ page }) => {
  await page.route('**/media/**', (route) =>
    route.fulfill({ contentType: 'image/svg+xml', body: '<svg xmlns="http://www.w3.org/2000/svg" width="80" height="80"><rect width="80" height="80" fill="#5a7a6a"/></svg>' }),
  )

  const WORLD = {
    id: 'lowtown-abc123',
    name: 'Lowtown',
    description: 'The guild quarter after dark.',
    characters: { Elira: 'elira-1', Rook: 'rook-1' },
    sessions: 1,
  }
  let createOpts: Record<string, unknown> | null = null
  const saves: unknown[] = []
  const mock = await installMockBackend(page, {
    respond: (method, params) => {
      if (method === 'cards.list')
        return {
          cards: [
            { id: 'elira-1', name: 'Elira', greetings: 1, creator: 'drew' },
            // A character the World invented: badged, and still on the shelf.
            { id: 'kira-1', name: 'Kira', greetings: 1, world_of: 'lowtown-abc123' },
          ],
        }
      if (method === 'personas.list') return { personas: [] }
      if (method === 'worlds.list') return { worlds: [WORLD] }
      if (method === 'sessions.list')
        return { sessions: [{ id: 'oldsess', title: 'The Ledger Job', world: 'lowtown-abc123', experience: 'chat', messages: 12, usage: {} }] }
      if (method === 'cards.get') return { id: 'elira-1', name: 'Elira', greetings: 1, avatar_url: '/media/cards/elira-1', raw: {} }
      if (method === 'backgrounds.list') return { backgrounds: [] }
      if (method === 'sessions.create') {
        createOpts = params as Record<string, unknown>
        return { session: { id: SMOKE_SESSION, title: 'Lowtown', experience: 'chat', card: (params as { card?: string }).card, world: (params as { world?: string }).world } }
      }
      if (method === 'worlds.save') {
        saves.push(params)
        return { id: 'lowtown-abc123', name: 'Lowtown' }
      }
      return undefined
    },
  })

  await page.goto('/stage.html')

  // A World-born character stays on the shelf and says which World made it.
  // Shot because the badge is a LAYOUT question the assertions cannot see: it
  // sits on its own line above the creator/count row, and a long World name has
  // to truncate rather than make one tile taller than its neighbours.
  await expect(page.locator('.stage-card', { hasText: 'Kira' })).toBeVisible()
  await expect(page.locator('.stage-card__world')).toHaveCount(1)
  await page.screenshot({ path: 'test-results/card-world-badge.png' })

  // The shelf renders the saved World with its census.
  const worldTile = page.locator('.stage-world', { hasText: 'Lowtown' })
  await expect(worldTile).toBeVisible()
  await expect(worldTile).toContainText('2 characters')
  await expect(worldTile).toContainText('1 chat')

  // The World's own screen: its cast, and — on its own tab — the scenes played
  // in it. This used to be a drawer thrown over the Library.
  await worldTile.click()
  await expect(page.locator('.stage-worldstudio')).toBeVisible()
  await expect(page.locator('.stage-worldroster__row')).toHaveCount(2)
  await page.locator('.stage-studio__tab', { hasText: 'Scenes' }).click()
  await expect(page.locator('.stage-yourchats__title')).toHaveText('The Ledger Job')

  // "Chat" as Rook → a session created INSIDE the World.
  await page.locator('.stage-studio__tab', { hasText: 'Cast' }).click()
  await page.locator('.stage-worldroster__row', { hasText: 'Rook' }).getByTitle('New chat with Rook').click()
  await expect.poll(() => createOpts).toMatchObject({ experience: 'chat', card: 'rook-1', world: 'lowtown-abc123' })
  await expect(page.locator('.stage-composer')).toBeVisible()
  await mock.subscribed

  // This member session's World tab offers save-back (no name field).
  mock.pushEvent(
    {
      type: 'snapshot',
      snapshot: {
        session: { id: SMOKE_SESSION, title: 'Lowtown', experience: 'chat', card: 'rook-1', world: 'lowtown-abc123', cast: { Elira: 'elira-1' } },
        epoch: 1,
        base: 0,
        total: 0,
        messages: [],
        busy: false,
      },
    },
    SMOKE_SESSION,
  )
  await page.locator('.stage-steer-btn').click()
  await page.locator('.stage-drawer__tab', { hasText: 'World' }).click()
  await page.locator('button', { hasText: 'Save changes to World' }).click()
  await expect.poll(() => saves.length).toBe(1)
  expect(saves[0]).toEqual({})

  // An unsaved session's World tab promotes with a name.
  mock.pushEvent(
    {
      type: 'snapshot',
      snapshot: {
        session: { id: SMOKE_SESSION, title: 'Lowtown', experience: 'chat', card: 'rook-1', cast: { Elira: 'elira-1' } },
        epoch: 2,
        base: 0,
        total: 0,
        messages: [],
        busy: false,
      },
    },
    SMOKE_SESSION,
  )
  await page.locator('.stage-worldsave input').fill('Lowtown at War')
  await page.locator('button', { hasText: 'Save as World' }).click()
  await expect.poll(() => saves.length).toBe(2)
  expect(saves[1]).toEqual({ name: 'Lowtown at War' })
})
