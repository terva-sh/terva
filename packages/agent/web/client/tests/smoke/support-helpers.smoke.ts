import { test, expect } from '@playwright/test'
import { installStageBackend, stubMedia } from './support'

// installStageBackend answers cards.list and personas.list for every call it
// wraps, and a test's OWN respond runs first and falls through on undefined.
//
// That precedence is what made switching fifty files safe without reading each
// one: a call that already named its shelf keeps naming it, and a call that did
// not gets the default. It is asserted here rather than left implicit in fifty
// tests passing, because "they all still pass" cannot distinguish "the override
// works" from "no test depended on it".
test('smoke support: a test respond outranks the Stage floor', async ({ page }) => {
  await stubMedia(page)
  await installStageBackend(page, {
    // Not the helper's default shelf. If the floor won, the Library would show
    // Ivy and this test would say so by name.
    respond: (method) => {
      if (method === 'cards.list') return { cards: [{ id: 'own-1', name: 'Overridden', greetings: 1 }] }
      return undefined
    },
  })
  await page.goto('/stage.html')

  await expect(page.locator('.stage-library')).toBeVisible()
  await expect(
    page.getByText('Overridden'),
    "the test's own cards.list did not win — installStageBackend is answering over it, " +
      'which would have changed what every migrated file sees',
  ).toBeVisible()
  await expect(
    page.getByText('Ivy', { exact: true }),
    "the helper's default shelf reached a test that named its own",
  ).toHaveCount(0)
})

// The other half: a call that names nothing gets the floor, which is what lets a
// migrated file delete an arm rather than restate it.
test('smoke support: a call that names nothing gets the default shelf', async ({ page }) => {
  await stubMedia(page)
  await installStageBackend(page)
  await page.goto('/stage.html')

  await expect(page.locator('.stage-library')).toBeVisible()
  await expect(
    page.getByText('Ivy', { exact: true }),
    'the default shelf did not arrive — every file whose arm was deleted as redundant ' +
      'is now booting an empty Library',
  ).toBeVisible()
})

// The persona roster is the floor's other half, and an empty one renders the
// same as an absent one — so a test that boots with no personas cannot tell
// whether the helper served them. This one names a persona, which also gives
// the `personas` option its only caller: an option with none is a shape nobody
// has checked against the surface that consumes it.
test('smoke support: the persona roster reaches the Library', async ({ page }) => {
  await stubMedia(page)
  await installStageBackend(page, {
    cards: [],
    personas: [
      {
        name: 'Kertoja',
        ref: 'crew:kertoja',
        namespace: 'crew',
        origin: 'built-in',
        emoji: '📖',
        specialty: 'Narration',
        summary: 'The narrator who weaves a scene from what the cast returns.',
        immersive: true,
      },
    ],
  })
  await page.goto('/stage.html')

  await expect(
    page.locator('.stage-persona', { hasText: 'Kertoja' }),
    'the roster did not arrive — installStageBackend is not answering personas.list, so ' +
      'every migrated file whose arm was deleted as redundant now boots without one',
  ).toBeVisible()
})
