import { test, expect } from '@playwright/test'
import { SMOKE_SESSION, installStageBackend, stubMedia } from './support'

// Regression for the release-review blocker: during a live turn the echoed user
// row and the streaming assistant row arrive as OPTIMISTIC, unplaced items (no
// idx, since the end-of-turn snapshot has not merged them yet). A stale editing
// guard (editing?.idx === it.idx) was true for those rows — undefined === undefined
// — so the whole streaming window rendered as empty <textarea> edit boxes instead
// of the text. This drives exactly that state (stream, no trailing snapshot) and
// asserts the words show and no edit box appears.
test('stage: a streaming turn renders text, not empty edit boxes', async ({ page }) => {
  await stubMedia(page)

  const mock = await installStageBackend(page, {
    cards: [{ id: 'seraphina-1', name: 'Seraphina', greetings: 1 }],
    respond: (method) => {
      if (method === 'sessions.create') return { session: { id: SMOKE_SESSION, title: 'Seraphina', experience: 'chat', card: 'seraphina-1' } }
      if (method === 'cards.get') return { id: 'seraphina-1', name: 'Seraphina', greetings: 1, raw: {} }
      return undefined
    },
  })

  await page.goto('/stage.html')
  await page.locator('.stage-card').first().click()
  await expect(page.locator('.stage-composer')).toBeVisible()
  await mock.subscribed

  // The live turn, WITHOUT a trailing snapshot: a user echo, then streamed
  // assistant deltas. Both land as unplaced rows (the bug's trigger).
  mock.pushEvent({ type: 'user_message', message: { role: 'user', content: [{ type: 'text', text: '"Hello there."' }] } }, SMOKE_SESSION)
  mock.pushEvent({ type: 'text_delta', delta: 'General ' }, SMOKE_SESSION)
  mock.pushEvent({ type: 'text_delta', delta: 'Kenobi.' }, SMOKE_SESSION)

  // The words are on screen as prose bubbles...
  await expect(page.locator('.stage-row--user .stage-bubble')).toContainText('Hello there.')
  await expect(page.locator('.stage-row--assistant .stage-bubble')).toContainText('General Kenobi.')
  // ...and NOT a single edit box was rendered for the streaming rows.
  await expect(page.locator('.stage-edit textarea')).toHaveCount(0)
})
