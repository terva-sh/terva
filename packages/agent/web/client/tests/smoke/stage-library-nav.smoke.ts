import { test, expect } from '@playwright/test'
import { installMockBackend, SMOKE_SESSION } from './support'

// Library navigation (rough-edges #2/#3): tapping a character that already has
// chats surfaces them to resume (auto-new only when there are none), and a
// "Your chats" list gathers every immersive session most-recent first. Driven
// against a mock card + session library.

const CARDS = [
  { id: 'card-1', name: 'Ivy', greetings: 1 },
  { id: 'card-2', name: 'Rook', greetings: 1 },
]

// Ivy has two chats (server lists most-recent first); Rook has none.
const SESSIONS = [
  { id: 'sess-newer', title: 'Rainy afternoon', card: 'card-1', experience: 'chat', messages: 12, updated: '2026-07-18T10:00:00Z' },
  { id: 'sess-play', title: 'The heist', card: 'card-1', experience: 'play', messages: 30, updated: '2026-07-17T12:00:00Z' },
]

function backend(page: Parameters<typeof installMockBackend>[0], onCreate?: (params: { card?: string }) => void) {
  return installMockBackend(page, {
    respond: (method, params) => {
      if (method === 'cards.list') return { cards: CARDS }
      if (method === 'personas.list') return { personas: [] }
      if (method === 'sessions.list') return { sessions: SESSIONS }
      if (method === 'sessions.create') {
        onCreate?.(params as { card?: string })
        return { session: { id: SMOKE_SESSION, title: 'Ivy', experience: 'chat', card: (params as { card?: string }).card } }
      }
      return undefined
    },
  })
}

const SNAPSHOT = {
  type: 'snapshot',
  snapshot: {
    session: { id: SMOKE_SESSION, title: 'Ivy', experience: 'chat', card: 'card-1' },
    epoch: 1,
    base: 0,
    total: 1,
    messages: [{ role: 'assistant', content: [{ type: 'text', text: 'Hi.' }] }],
    busy: false,
  },
}

test('stage: a character with chats opens its chat list, not a new chat', async ({ page }) => {
  let created: { card?: string } | null = null
  const mock = await backend(page, (p) => (created = p))

  await page.goto('/stage.html')
  await expect(page.locator('.stage-card')).toHaveCount(2)

  // Tapping Ivy (who has chats) lands on the resume sheet — its two chats, most
  // recent first — and does NOT create a session or open the composer.
  await page.locator('.stage-card', { hasText: 'Ivy' }).click()
  await expect(page.locator('.stage-charchats__list')).toBeVisible()
  await expect(page.locator('.stage-charchats__item')).toHaveCount(2)
  await expect(page.locator('.stage-charchats__item').first()).toContainText('Rainy afternoon')
  await expect(page.locator('.stage-composer')).toHaveCount(0)
  expect(created).toBeNull()
  if (process.env.SHEET_SHOT) await page.screenshot({ path: `${process.env.SHEET_SHOT}.png` })

  // "+ New chat" from the sheet starts a fresh chat with that character.
  await page.locator('.stage-sheet--chats .stage-sheet__start', { hasText: 'New chat' }).click()
  await expect.poll(() => created).toEqual({ experience: 'chat', card: 'card-1', greeting: 0 })
  await expect(page.locator('.stage-composer')).toBeVisible()
  await mock.subscribed
  mock.pushEvent(SNAPSHOT)
})

test('stage: a character with no chats starts a fresh chat directly', async ({ page }) => {
  let created: { card?: string } | null = null
  const mock = await backend(page, (p) => (created = p))

  await page.goto('/stage.html')
  await expect(page.locator('.stage-card')).toHaveCount(2)

  // Rook has no chats → tapping goes straight into a new chat, no resume sheet.
  await page.locator('.stage-card', { hasText: 'Rook' }).click()
  await expect(page.locator('.stage-composer')).toBeVisible()
  await expect(page.locator('.stage-charchats__list')).toHaveCount(0)
  await expect.poll(() => created).toEqual({ experience: 'chat', card: 'card-2', greeting: 0 })
  await mock.subscribed
  mock.pushEvent(SNAPSHOT)
})

test('stage: "Your chats" lists every immersive session, resumable', async ({ page }) => {
  const mock = await backend(page)

  await page.goto('/stage.html')
  await expect(page.locator('.stage-card')).toHaveCount(2)

  // Every immersive session appears, most-recent first, tagged with its character.
  await expect(page.locator('.stage-yourchats__item')).toHaveCount(2)
  await expect(page.locator('.stage-yourchats__item').first()).toContainText('Rainy afternoon')
  await expect(page.locator('.stage-yourchats__item').first()).toContainText('Ivy')
  await expect(page.locator('.stage-yourchats__exp')).toContainText('play')
  if (process.env.NAV_SHOT) await page.screenshot({ path: `${process.env.NAV_SHOT}.png` })

  // Tapping a chat resumes it (opens the composer on that session).
  await page.locator('.stage-yourchats__item').first().click()
  await expect(page.locator('.stage-composer')).toBeVisible()
  await mock.subscribed
  mock.pushEvent(SNAPSHOT, 'sess-newer')
})
