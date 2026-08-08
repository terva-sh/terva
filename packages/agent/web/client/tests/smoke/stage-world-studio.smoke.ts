import { test, expect } from '@playwright/test'
import { installMockBackend } from './support'

// The World studio (WS-1/WS-2): a World's cast and lorebook edited from the
// shelf with NO SESSION OPEN.
//
// That is the property this smoke exists for. The component tests already cover
// which verb each control sends; what only a browser can show is that the whole
// round trip works from a cold Library — no chat created, no session in any
// frame — because before this the only way to change a World's contents was to
// open a scene in it first.
test('stage: a World is edited from the shelf with no session', async ({ page }) => {
  await page.route('**/media/**', (route) =>
    route.fulfill({ contentType: 'image/svg+xml', body: '<svg xmlns="http://www.w3.org/2000/svg" width="80" height="80"><rect width="80" height="80" fill="#5a7a6a"/></svg>' }),
  )

  let world = {
    id: 'lowtown-abc123',
    name: 'Lowtown',
    description: 'The guild quarter after dark.',
    characters: { Elira: 'elira-1' } as Record<string, string>,
    lore: [{ name: 'The curfew', keys: ['curfew'], content: 'The bells ring at dusk.' }] as Record<string, unknown>[],
    coordination: '',
  }
  // Every call the studio makes, with the session the frame carried.
  const calls: { method: string; params: Record<string, unknown>; sess?: string }[] = []

  await installMockBackend(page, {
    respond: (method, params, sess) => {
      calls.push({ method, params: (params ?? {}) as Record<string, unknown>, sess })
      const p = (params ?? {}) as Record<string, unknown>
      switch (method) {
        case 'cards.list':
          return {
            cards: [
              { id: 'elira-1', name: 'Elira', greetings: 1, avatar_url: '/media/cards/elira-1' },
              { id: 'rook-1', name: 'Rook', greetings: 1 },
            ],
          }
        case 'personas.list':
          return { personas: [] }
        case 'sessions.list':
          return { sessions: [] }
        case 'worlds.list':
          return { worlds: [world] }
        case 'worlds.add_character':
          world = { ...world, characters: { ...world.characters, [p.name as string]: p.ref as string } }
          return world
        case 'worlds.remove_character': {
          const next = { ...world.characters }
          delete next[p.name as string]
          world = { ...world, characters: next }
          return world
        }
        case 'worlds.lore.put':
          world = { ...world, lore: [...world.lore, p.entry as Record<string, unknown>] }
          return world
        case 'worlds.delete':
          return {}
        case 'worlds.set':
          world = { ...world, coordination: p.coordination as string }
          return world
        case 'worlds.set_model':
          world = { ...world, model: { provider: p.provider as string, model: p.model as string } }
          return world
        case 'models.list':
          return { models: [{ id: 'gpt-5', provider: 'openai' }, { id: 'claude-opus-4-8', provider: 'anthropic' }] }
        case 'models.default_for':
          return p.world
            ? { provider: 'openai', model: 'gpt-5', source: 'world' }
            : { provider: 'openai', model: 'gpt-5', source: 'workspace' }
        default:
          return undefined
      }
    },
  })

  await page.goto('/stage.html')

  // The shelf's own delete. Shot before entering, because the regression this
  // guards is that nothing on the SHELF said the operation existed — a screenshot
  // of the studio would have looked fine throughout.
  await expect(page.locator('.stage-world__del')).toHaveCount(1)
  await page.screenshot({ path: 'test-results/world-shelf-delete.png' })

  // Into the World from the shelf. No chat has been opened.
  await page.locator('.stage-world', { hasText: 'Lowtown' }).click()
  await expect(page.locator('.stage-worldstudio')).toBeVisible()
  await expect(page.locator('.stage-worldroster__row')).toHaveCount(1)

  // Add Rook to the cast. Picking the card fills the roster name.
  await page.locator('.stage-worldroster__add select').selectOption('rook-1')
  await expect(page.locator('.stage-worldroster__add input')).toHaveValue('Rook')
  await page.locator('.stage-worldroster__add button').click()
  await expect(page.locator('.stage-worldroster__row')).toHaveCount(2)

  // Coordination is a World setting: it decides who answers in every scene
  // started here, and it is reachable without being in one.
  await page.locator('.stage-worldstudio__coord select').selectOption('focus:Rook')
  await expect
    .poll(() => calls.filter((c) => c.method === 'worlds.set').map((c) => c.params.coordination))
    .toEqual(['focus:Rook'])

  // The World's own default model — the middle rung of card → world →
  // workspace. Opening the list in the foot is the layout case worth a browser:
  // the foot is the bottom of a column, and a list that grew unbounded there
  // would squeeze the pane above it.
  await page.locator('.stage-worldstudio__model .stage-modelpick__current').click()
  await expect(page.locator('.stage-worldstudio__model .stage-modelpick__list')).toBeVisible()
  await expect(page.locator('.stage-worldroster')).toBeVisible()
  // Wait for the catalog before shooting — models.list is fetched lazily on
  // open, so a screenshot taken the instant the list appears catches the empty
  // state and would quietly become the reference for how this looks.
  await expect(page.locator('.stage-worldstudio__model .stage-modelpick__row')).toHaveCount(3)
  await page.screenshot({ path: 'test-results/world-model-picker.png' })
  await page.locator('.stage-worldstudio__model .stage-modelpick__row', { hasText: 'claude-opus-4-8' }).click()
  await expect
    .poll(() => calls.filter((c) => c.method === 'worlds.set_model').map((c) => c.params.model))
    .toEqual(['claude-opus-4-8'])

  // The lorebook — the surface that previously existed only inside a session.
  await page.locator('.stage-studio__tab', { hasText: 'Lore' }).click()
  await expect(page.locator('.stage-lorebook__row')).toHaveCount(1)
  await page.locator('.stage-lorebook__new').click()
  await page.locator('.stage-lorebook__field input').first().fill('The harbour')
  await page.locator('.stage-lorebook__field textarea').fill('Tar, rope, and nobody asking names.')
  await page.locator('.stage-lorebook__field input').nth(2).fill('harbour, docks')
  await page.locator('.stage-lorebook__save').click()
  await expect(page.locator('.stage-lorebook__row')).toHaveCount(2)

  // The point of the whole screen: not one of those writes carried a session,
  // and no session was ever created.
  const writes = calls.filter((c) => c.method.startsWith('worlds.') && c.method !== 'worlds.list')
  expect(writes.length).toBeGreaterThanOrEqual(3)
  expect(writes.every((c) => !c.sess)).toBe(true)
  expect(calls.some((c) => c.method === 'sessions.create')).toBe(false)
})
