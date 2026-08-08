import { test, expect } from '@playwright/test'
import { installMockBackend } from './support'

// "Recently updated" — the character you were last WORKING on, as opposed to the
// one that arrived last ('added') or the one you last played ('used').
//
// The fixture is built so every mode yields a DIFFERENT order. That is the whole
// point: with cards whose added and updated agree, a comparator that read the
// wrong field — or one that never ran, leaving the previous mode in place —
// would produce the expected order anyway and the test would pass for no reason.
//
//   name   A→Z    : Ash,  Bree, Cade, Dusk
//   added  newest : Cade, Ash,  Bree        (Dusk last: no dates at all)
//   updated newest: Bree, Ash,  Cade        (Dusk last: no updated)
const CARDS = [
  { id: 'ash-1', name: 'Ash', greetings: 1, added: '2026-01-03T00:00:00Z', updated: '2026-01-04T00:00:00Z' },
  { id: 'bree-1', name: 'Bree', greetings: 1, added: '2026-01-01T00:00:00Z', updated: '2026-01-09T00:00:00Z' },
  { id: 'cade-1', name: 'Cade', greetings: 1, added: '2026-01-05T00:00:00Z', updated: '2026-01-02T00:00:00Z' },
  // A daemon too old to send `updated`. It must not break the sort, and it must
  // land at the bottom rather than being treated as freshly updated.
  { id: 'dusk-1', name: 'Dusk', greetings: 1 },
]

// Must track CARD_SORT_KEY in Library.tsx. The version suffix is the whole
// mechanism by which the new default reaches a library that already has a
// preference written on its behalf — see the two tests at the bottom.
const SORT_KEY = 'terva_card_sort_2'
const OLD_SORT_KEY = 'terva_card_sort'

async function names(page: import('@playwright/test').Page) {
  return page.locator('.stage-card__name').allInnerTexts()
}

async function mockLibrary(page: import('@playwright/test').Page) {
  await installMockBackend(page, {
    respond: (method) => {
      if (method === 'cards.list') return { cards: CARDS }
      if (method === 'personas.list') return { personas: [] }
      if (method === 'sessions.list') return { sessions: [] }
      if (method === 'worlds.list') return { worlds: [] }
      return undefined
    },
  })
}

test('stage: characters can be ordered by when they were last updated', async ({ page }) => {
  await mockLibrary(page)
  await page.goto('/stage.html')
  await expect(page.locator('.stage-card__name').first()).toBeVisible()

  const mode = page.locator('.stage-cardsort__mode')

  // Anchor on a mode whose order is known and DIFFERENT, so the assertion below
  // is a change rather than a coincidence.
  await mode.selectOption('added')
  expect(await names(page)).toEqual(['Cade', 'Ash', 'Bree', 'Dusk'])

  await mode.selectOption('updated')
  expect(await names(page)).toEqual(['Bree', 'Ash', 'Cade', 'Dusk'])

  // The direction toggle flips it. Dusk leads because "no date" is the far end
  // of the same axis, not a separate bucket.
  await page.locator('.stage-cardsort__dir').click()
  expect(await names(page)).toEqual(['Dusk', 'Cade', 'Ash', 'Bree'])
  // Flip back, and assert the restored order rather than just clicking, so the
  // reload below cannot be reading a half-applied state.
  await page.locator('.stage-cardsort__dir').click()
  expect(await names(page)).toEqual(['Bree', 'Ash', 'Cade', 'Dusk'])

  // The choice is written synchronously in the change handler, so by the time
  // the click above has resolved it is already on disk — no wait needed, and
  // asserting it directly is what pins that. (It used to be written from an
  // effect, and a reload issued straight after a click raced it about one run in
  // five; the wait that hid that is not needed and would hide it again.)
  const stored = await page.evaluate((k) => localStorage.getItem(k), SORT_KEY)
  expect(JSON.parse(stored ?? '')).toEqual({ mode: 'updated', reversed: false })

  // Persisted like the other modes: a reload comes back on "recently updated"
  // rather than silently reverting and re-shuffling the shelf.
  await page.reload()
  await expect(page.locator('.stage-card__name').first()).toBeVisible()
  await expect(mode).toHaveValue('updated')
  expect(await names(page)).toEqual(['Bree', 'Ash', 'Cade', 'Dusk'])
})

test('stage: favorites stay pinned above the recently-updated order', async ({ page }) => {
  await installMockBackend(page, {
    respond: (method) => {
      // Cade is the OLDEST update, so if favorites did not float it would sort
      // third. Pinning is what puts it first, and the rest keep their order.
      if (method === 'cards.list')
        return { cards: CARDS.map((c) => (c.id === 'cade-1' ? { ...c, favorite: true } : c)) }
      if (method === 'personas.list') return { personas: [] }
      if (method === 'sessions.list') return { sessions: [] }
      if (method === 'worlds.list') return { worlds: [] }
      return undefined
    },
  })

  await page.goto('/stage.html')
  await expect(page.locator('.stage-card__name').first()).toBeVisible()
  await page.locator('.stage-cardsort__mode').selectOption('updated')

  expect(await names(page)).toEqual(['Cade', 'Bree', 'Ash', 'Dusk'])
})

// What "make it the default" has to mean in practice.
test('stage: a fresh library opens on recently-updated, untouched', async ({ page }) => {
  await mockLibrary(page)
  await page.goto('/stage.html')
  await expect(page.locator('.stage-card__name').first()).toBeVisible()

  // No interaction with the control at all — this is the state you land in.
  await expect(page.locator('.stage-cardsort__mode')).toHaveValue('updated')
  expect(await names(page)).toEqual(['Bree', 'Ash', 'Cade', 'Dusk'])

  // And nothing was written. Absence means "no preference expressed", which is
  // what lets a future default change reach this reader; a value stored on mount
  // would pin them to today's default forever. That is exactly the bug that made
  // the key below necessary, so it is asserted rather than assumed.
  expect(await page.evaluate((k) => localStorage.getItem(k), SORT_KEY)).toBeNull()
})

// The reason CARD_SORT_KEY carries a version. Every library that has ever been
// opened has the OLD key written on its behalf — by an effect that fired on
// mount, before the reader chose anything. Reading that key would hand those
// users the old default forever, and the change would be invisible to precisely
// the people who already use Stage.
test('stage: a preference written by the old build does not pin the shelf', async ({ page }) => {
  await mockLibrary(page)
  await page.addInitScript(
    ([oldKey]) => localStorage.setItem(oldKey, JSON.stringify({ mode: 'name', reversed: false })),
    [OLD_SORT_KEY],
  )
  await page.goto('/stage.html')
  await expect(page.locator('.stage-card__name').first()).toBeVisible()

  // Alphabetical would be Ash, Bree, Cade, Dusk — which is what the stale value
  // asks for, and what a same-key implementation would give.
  await expect(page.locator('.stage-cardsort__mode')).toHaveValue('updated')
  expect(await names(page)).toEqual(['Bree', 'Ash', 'Cade', 'Dusk'])
})

// ...but a choice made under the NEW key is still a choice, and outranks the
// default. Without this, "ignore the old key" could be implemented as "ignore
// storage", and both tests above would still pass.
test('stage: an explicit choice still wins over the default', async ({ page }) => {
  await mockLibrary(page)
  await page.addInitScript(
    ([key]) => localStorage.setItem(key, JSON.stringify({ mode: 'name', reversed: false })),
    [SORT_KEY],
  )
  await page.goto('/stage.html')
  await expect(page.locator('.stage-card__name').first()).toBeVisible()

  await expect(page.locator('.stage-cardsort__mode')).toHaveValue('name')
  expect(await names(page)).toEqual(['Ash', 'Bree', 'Cade', 'Dusk'])
})
