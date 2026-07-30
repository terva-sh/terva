import { test, expect } from '@playwright/test'
import { installMockBackend } from './support'

// Finding and filing cards in a library too big to scroll.
//
// The two controls this covers exist for one scenario: a library that arrives
// as a flat import of hundreds, all of it needing to be organized. Search
// narrows to a cluster (name, creator, or the TAGS an imported card came with);
// the "Ungrouped" chip is what says how much is left to file, and shrinks as
// batches land.
//
// Set CF_SHOT=<dir> to capture screenshots — the header row this adds a field to
// already carried five controls, and a wrapped or overflowing row is precisely
// what a green vitest run does not notice.

const card = (id: string, name: string, over: Record<string, unknown> = {}) => ({
  id,
  name,
  avatar_url: `/media/cards/${id}`,
  greetings: 1,
  ...over,
})

// A small library standing in for a migrated one: two fantasy cards that share
// a tag, one sci-fi, one already filed into a group.
const CARDS = [
  card('c1', 'Elowen', { tags: ['fantasy', 'elf'], creator: 'mapmaker' }),
  card('c2', 'Thorne', { tags: ['fantasy', 'knight'] }),
  card('c3', 'Vex', { tags: ['scifi'], creator: 'someone' }),
  card('c4', 'Kobeni', { tags: ['comedy'] }),
]
const GROUPS = [{ id: 'g1', name: 'Filed', members: ['c4'] }]

async function library(page: import('@playwright/test').Page) {
  await page.route('**/media/**', (r) =>
    r.fulfill({
      contentType: 'image/svg+xml',
      body: '<svg xmlns="http://www.w3.org/2000/svg" width="80" height="80"><rect width="80" height="80" fill="#3f5a3a"/></svg>',
    }),
  )
  await installMockBackend(page, {
    respond: (method) => {
      if (method === 'cards.list') return { cards: CARDS }
      if (method === 'cardgroups.list') return { groups: GROUPS }
      if (method === 'personas.list') return { personas: [] }
      if (method === 'sessions.list') return { sessions: [] }
      return {}
    },
  })
  await page.goto('/stage')
  await expect(page.getByText('Elowen')).toBeVisible()
}

test('stage: search narrows the library by name, creator and tag', async ({ page }) => {
  await library(page)
  const search = page.getByPlaceholder('Search characters')
  await expect(search).toBeVisible()

  // By name.
  await search.fill('elowen')
  await expect(page.getByText('Elowen')).toBeVisible()
  await expect(page.getByText('Thorne')).toHaveCount(0)

  // By TAG — the migration lever: an imported library arrives tagged, so a
  // query can pull out the cluster its author already drew.
  await search.fill('fantasy')
  await expect(page.getByText('Elowen')).toBeVisible()
  await expect(page.getByText('Thorne')).toBeVisible()
  await expect(page.getByText('Vex')).toHaveCount(0)

  // By creator.
  await search.fill('mapmaker')
  await expect(page.getByText('Elowen')).toBeVisible()
  await expect(page.getByText('Thorne')).toHaveCount(0)

  // Terms AND, so each word narrows rather than widens.
  await search.fill('fantasy knight')
  await expect(page.getByText('Thorne')).toBeVisible()
  await expect(page.getByText('Elowen')).toHaveCount(0)

  // A query matching nothing says so, and names the query rather than sending
  // the user to clear a group chip that is not on.
  await search.fill('zzzz')
  await expect(page.getByText(/No characters match/)).toBeVisible()

  await search.fill('')
  await expect(page.getByText('Vex')).toBeVisible()

  if (process.env.CF_SHOT) {
    await page.screenshot({ path: `${process.env.CF_SHOT}/card-find-desktop.png`, fullPage: true })
  }
})

test('stage: the Ungrouped chip counts what is still unfiled and filters to it', async ({ page }) => {
  await library(page)

  // Three of the four cards are in no group; the count is the work remaining.
  const chip = page.locator('.stage-groupchip', { hasText: 'Ungrouped' })
  await expect(chip).toBeVisible()
  await expect(chip).toContainText('3')

  // Filtering to it shows exactly the unfiled cards — the filed one drops out.
  await chip.getByRole('button').first().click()
  await expect(page.getByText('Elowen')).toBeVisible()
  await expect(page.getByText('Kobeni')).toHaveCount(0)

  // It carries no edit affordance: a derived chip is nothing to rename.
  await expect(chip.locator('.stage-groupchip__manage')).toHaveCount(0)

  if (process.env.CF_SHOT) {
    await page.screenshot({ path: `${process.env.CF_SHOT}/card-find-ungrouped.png`, fullPage: true })
  }
})

// The header row now holds search, a sort select, a direction button, Select,
// + New character and + Import. On a phone that has to wrap rather than
// overflow: a row that scrolls sideways takes the whole page with it.
test('stage: the library header does not overflow a phone', async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 })
  await library(page)
  await expect(page.getByPlaceholder('Search characters')).toBeVisible()

  const overflow = await page.evaluate(() => {
    const doc = document.documentElement
    return { scrollW: doc.scrollWidth, clientW: doc.clientWidth }
  })
  expect(overflow.scrollW).toBeLessThanOrEqual(overflow.clientW)

  if (process.env.CF_SHOT) {
    await page.screenshot({ path: `${process.env.CF_SHOT}/card-find-phone.png`, fullPage: true })
  }
})
