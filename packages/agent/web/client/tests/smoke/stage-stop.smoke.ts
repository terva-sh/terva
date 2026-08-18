import { test, expect } from '@playwright/test'
import { SMOKE_SESSION, installStageBackend, stubMedia } from './support'

// The bottom-of-screen working signal and the Stop button.
//
// Before this, the only bottom-adjacent tell that anything was happening was the
// per-message streaming caret — which appears only once the FIRST token lands, so
// the whole provider round-trip showed nothing down here, and which lives on a
// transcript row, so it scrolls away when you read back. The header's "thinking…"
// is easy to miss. The throbber is driven off busy so it covers both gaps.
//
// Stop matters for more than impatience: post.line / cast.speak / ▶ all reject
// while a turn is in flight, so interrupting is the only way to slip a narrator
// line in before the model continues.
test('stage: a turn shows a throbber and can be stopped', async ({ page }) => {
  await stubMedia(page)

  let cancels = 0
  const mock = await installStageBackend(page, {
    cards: [{ id: 'kobeni-1', name: 'Kobeni', greetings: 1 }],
    respond: (method) => {
      if (method === 'sessions.create') return { session: { id: SMOKE_SESSION, title: 'The Fitting', experience: 'chat', card: 'kobeni-1' } }
      if (method === 'cards.get') return { id: 'kobeni-1', name: 'Kobeni', greetings: 1, raw: {} }
      if (method === 'cancel') {
        cancels++
        return {}
      }
      return undefined
    },
  })

  await page.goto('/stage.html')
  await page.locator('.stage-card').first().click()
  await expect(page.locator('.stage-composer')).toBeVisible()
  await mock.subscribed

  // Idle: no throbber, Send is the action.
  await expect(page.locator('.stage-working')).toHaveCount(0)
  await expect(page.locator('.stage-composer button', { hasText: 'Send' })).toBeVisible()

  await page.locator('.stage-composer textarea').fill('I sign both copies.')
  await page.locator('.stage-composer button', { hasText: 'Send' }).click()

  // Working: the throbber appears at the bottom BEFORE any token has streamed —
  // the gap the caret never covered — and Send has become Stop.
  await expect(page.locator('.stage-working')).toBeVisible()
  const stop = page.locator('.stage-stop-btn')
  await expect(stop).toBeVisible()
  await expect(page.locator('.stage-composer button', { hasText: 'Send' })).toHaveCount(0)

  await stop.click()
  await expect.poll(() => cancels).toBe(1)

  // The daemon settles the turn; the throbber goes and Send comes back, so the
  // user can now narrate into the scene they just interrupted.
  mock.pushEvent({ type: 'done' }, SMOKE_SESSION)
  await expect(page.locator('.stage-working')).toHaveCount(0)
  await expect(page.locator('.stage-composer button', { hasText: 'Send' })).toBeVisible()
})
