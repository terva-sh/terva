import { test, expect } from '@playwright/test'
import { installMockBackend, SMOKE_SESSION } from './support'

// Saved Worlds, W5b: the Library's Worlds section carries a bundle import
// button; the World sheet renames/describes (worlds.update), sets a cover,
// and exports the World as a downloadable bundle (worlds.export). Zero
// backend.
test('stage: World rename, cover, export bundle, and import', async ({ page }) => {
  await page.route('**/media/**', (route) =>
    route.fulfill({ contentType: 'image/svg+xml', body: '<svg xmlns="http://www.w3.org/2000/svg" width="80" height="80"><rect width="80" height="80" fill="#5a7a6a"/></svg>' }),
  )

  let world: Record<string, unknown> = {
    id: 'lowtown-abc123',
    name: 'Lowtown',
    description: 'The guild quarter after dark.',
    characters: { Elira: 'elira-1' },
    sessions: 0,
  }
  const updates: Record<string, unknown>[] = []
  const imports: Record<string, unknown>[] = []
  let createOpts: Record<string, unknown> | null = null
  let exported = 0
  await installMockBackend(page, {
    respond: (method, params) => {
      if (method === 'cards.list') return { cards: [] }
      if (method === 'personas.list') return { personas: [] }
      if (method === 'sessions.list') return { sessions: [] }
      if (method === 'backgrounds.list') return { backgrounds: [] }
      if (method === 'worlds.list') return { worlds: [world] }
      if (method === 'worlds.update') {
        const p = params as Record<string, unknown>
        updates.push(p)
        world = {
          ...world,
          ...(typeof p.name === 'string' && p.name !== '' ? { name: p.name } : {}),
          description: p.description,
          ...(p.cover ? { cover_url: '/media/worlds/lowtown-abc123' } : {}),
          ...(p.remove_cover ? { cover_url: undefined } : {}),
        }
        return world
      }
      if (method === 'worlds.export') {
        exported++
        // btoa-safe: the daemon sends base64([]byte) — a tiny JSON stands in.
        return { filename: 'Lowtown.world.json', mime_type: 'application/json', bytes: btoa('{"spec":"terva.world_bundle.v1"}') }
      }
      if (method === 'worlds.import') {
        imports.push(params as Record<string, unknown>)
        return { id: 'harbor-def456', name: 'Harbor' }
      }
      if (method === 'sessions.create') {
        createOpts = params as Record<string, unknown>
        return { session: { id: SMOKE_SESSION, title: 'Lowtown', experience: 'play', world: 'lowtown-abc123' } }
      }
      return undefined
    },
  })

  await page.goto('/stage.html')

  // The Worlds section renders (daemon supports it) with the import affordance.
  const worldTile = page.locator('.stage-world', { hasText: 'Lowtown' })
  await expect(worldTile).toBeVisible()

  // Rename + describe via the studio's ✎.
  await worldTile.click()
  const studio = page.locator('.stage-worldstudio')
  await expect(studio).toBeVisible()
  await studio.locator('button[title="Rename or describe this World"]').click()
  await studio.locator('.stage-worldedit__name').fill('Lowtown at Dusk')
  await studio.locator('.stage-worldedit__desc').fill('Grimmer still.')
  await studio.locator('.stage-worldedit__save').click()
  await expect.poll(() => updates.length).toBe(1)
  expect(updates[0]).toMatchObject({ id: 'lowtown-abc123', name: 'Lowtown at Dusk', description: 'Grimmer still.' })
  await expect(studio.locator('.stage-worldstudio__title h2')).toContainText('Lowtown at Dusk')

  // Set a cover: the file rides worlds.update as base64; the screen shows it.
  await studio
    .locator('.stage-worldsheet__coverbtn input[type="file"]')
    .setInputFiles({ name: 'cover.png', mimeType: 'image/png', buffer: Buffer.from('png-bytes') })
  await expect.poll(() => updates.length).toBe(2)
  expect(updates[1]).toMatchObject({ id: 'lowtown-abc123', cover: Buffer.from('png-bytes').toString('base64') })
  await expect(studio.locator('.stage-worldstudio__cover')).toBeVisible()

  // Export downloads the bundle.
  const download = page.waitForEvent('download')
  await studio.locator('.stage-worldsheet__export').click()
  expect((await download).suggestedFilename()).toBe('Lowtown.world.json')
  expect(exported).toBe(1)
  // Back to the Library — the studio is a screen now, so leaving it is a
  // navigation rather than dismissing a drawer.
  await studio.locator('.stage-back').click()

  // Import: a bundle file posts worlds.import (base64) and refreshes the shelf.
  const bundle = JSON.stringify({ spec: 'terva.world_bundle.v1', world: { name: 'Harbor' } })
  await page
    .locator('.stage-section-head', { hasText: 'Worlds' })
    .locator('input[type="file"]')
    .setInputFiles({ name: 'harbor.world.json', mimeType: 'application/json', buffer: Buffer.from(bundle) })
  await expect.poll(() => imports.length).toBe(1)
  expect(imports[0]).toMatchObject({ bytes: Buffer.from(bundle).toString('base64') })

  // 🎭 Play here (W6): a play session created INSIDE the World — the roster
  // warms as the director's actors server-side.
  await page.locator('.stage-world', { hasText: 'Lowtown' }).click()
  await studio.locator('.stage-worldsheet__play').click()
  await expect.poll(() => createOpts).toMatchObject({ experience: 'play', world: 'lowtown-abc123' })
  await expect(page.locator('.stage-composer')).toBeVisible()
})
