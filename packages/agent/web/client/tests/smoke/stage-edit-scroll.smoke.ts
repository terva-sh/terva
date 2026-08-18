import { test, expect, type Page } from '@playwright/test'
import { SMOKE_SESSION, installStageBackend } from './support'

// Two UI fixes: (1) a chat lands at the BOTTOM on load — where the composer is —
// not scrolled to the top; (2) editing a message opens a full-width box whose
// controls clear the scrollbar, instead of a box pinned to the bubble's narrow
// footprint. Zero backend.
function longChat() {
  const long =
    'I finish copying the added provisions onto the second sheet, compare the two line by line, and sign both before turning them toward her with the pen. "Read them once more."'
  const messages = []
  for (let i = 0; i < 8; i++) messages.push({ role: i % 2 ? 'user' : 'assistant', content: [{ type: 'text', text: `Line ${i}. ${long}` }] })
  return messages
}

async function openChat(page: Page) {
  const mock = await installStageBackend(page, {
    cards: [{ id: 'card-1', name: 'Kobeni', greetings: 1 }],
    respond: (method) => {
      if (method === 'cards.get') return { id: 'card-1', name: 'Kobeni', greetings: 1, raw: {} }
      if (method === 'sessions.create') return { session: { id: SMOKE_SESSION, title: 'Chat', experience: 'chat', card: 'card-1' } }
      return undefined
    },
  })
  await page.goto('/stage.html')
  await page.locator('.stage-card').first().click()
  await expect(page.locator('.stage-composer')).toBeVisible()
  await mock.subscribed
  const messages = longChat()
  mock.pushEvent(
    { type: 'snapshot', snapshot: { session: { id: SMOKE_SESSION, title: 'Chat', experience: 'chat', card: 'card-1' }, epoch: 1, base: 0, total: messages.length, messages, busy: false } },
    SMOKE_SESSION,
  )
  await expect(page.locator('.stage-bubble').last()).toBeVisible()
}

test('stage: a loaded chat lands at the bottom', async ({ page }) => {
  await openChat(page)
  // The transcript is scrolled to (near) its end, not sitting at the top.
  const atBottom = await page.locator('.stage-transcript').evaluate((el) => el.scrollHeight - el.scrollTop - el.clientHeight < 80)
  expect(atBottom).toBe(true)
  // The newest message is the one in view above the composer.
  await expect(page.locator('.stage-bubble').last()).toContainText('Line 7')
})

test('stage: the edit box spans the pane, clear of the scrollbar', async ({ page }) => {
  await openChat(page)
  const paneWidth = (await page.locator('.stage-transcript').boundingBox())!.width
  await page.locator('.stage-msgedit').last().click()
  const box = page.locator('.stage-edit textarea')
  await expect(box).toBeVisible()
  const editWidth = (await box.boundingBox())!.width
  // Full-width, not the old ~16rem (256px) min-width pin: at least 70% of the pane.
  expect(editWidth).toBeGreaterThan(paneWidth * 0.7)
})
