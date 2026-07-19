import { test, expect } from '@playwright/test'
import { installMockBackend } from './support'

// Deleting a session from Stage: a trash action on each "Your chats" row and in
// the character-chats sheet, wired to sessions.delete (target rides the frame's
// sess) with a confirm, then a re-list. Driven against a mock that drops deleted
// ids from sessions.list.
const CARDS = [{ id: 'card-1', name: 'Ivy', greetings: 1 }]

function mkBackend(page: Parameters<typeof installMockBackend>[0], deleted: Set<string>, captured: string[]) {
  const ALL = [
    { id: 'sess-a', title: 'Rainy afternoon', card: 'card-1', experience: 'chat', messages: 12, updated: '2026-07-18T10:00:00Z' },
    { id: 'sess-b', title: 'First meeting', card: 'card-1', experience: 'chat', messages: 4, updated: '2026-07-17T09:00:00Z' },
  ]
  return installMockBackend(page, {
    respond: (method, _params, sess) => {
      if (method === 'cards.list') return { cards: CARDS }
      if (method === 'personas.list') return { personas: [] }
      if (method === 'sessions.list') return { sessions: ALL.filter((s) => !deleted.has(s.id)) }
      if (method === 'sessions.delete') {
        if (sess) {
          captured.push(sess)
          deleted.add(sess)
        }
        return {}
      }
      return undefined
    },
  })
}

test('stage: delete a chat from the "Your chats" list', async ({ page }) => {
  const deleted = new Set<string>()
  const captured: string[] = []
  page.on('dialog', (d) => d.accept()) // the confirm()
  await mkBackend(page, deleted, captured)

  await page.goto('/stage.html')
  await expect(page.locator('.stage-yourchats__row')).toHaveCount(2)

  // Delete the most-recent row (sess-a). Hover to reveal the trash, then click.
  const row = page.locator('.stage-yourchats__row', { hasText: 'Rainy afternoon' })
  await row.hover()
  if (process.env.DEL_SHOT) await page.screenshot({ path: `${process.env.DEL_SHOT}.png` })
  await row.locator('.stage-yourchats__del').click()

  await expect.poll(() => captured).toEqual(['sess-a'])
  // The list re-fetches and the deleted row is gone.
  await expect(page.locator('.stage-yourchats__row')).toHaveCount(1)
  await expect(page.locator('.stage-yourchats')).not.toContainText('Rainy afternoon')
})

test('stage: delete a chat from the character-chats sheet', async ({ page }) => {
  const deleted = new Set<string>()
  const captured: string[] = []
  page.on('dialog', (d) => d.accept())
  await mkBackend(page, deleted, captured)

  await page.goto('/stage.html')
  await expect(page.locator('.stage-card')).toHaveCount(1)

  // Ivy has chats → tapping opens the resume sheet.
  await page.locator('.stage-card', { hasText: 'Ivy' }).click()
  await expect(page.locator('.stage-charchats__row')).toHaveCount(2)

  const row = page.locator('.stage-charchats__row', { hasText: 'First meeting' })
  await row.hover()
  await row.locator('.stage-charchats__del').click()

  await expect.poll(() => captured).toEqual(['sess-b'])
  // The sheet's list reflects the re-fetch (chatsForCard recomputes).
  await expect(page.locator('.stage-charchats__row')).toHaveCount(1)
})
