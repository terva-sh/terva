import { test, expect } from '@playwright/test'
import { installMockBackend, SMOKE_SESSION } from './support'

// Advance (▶): the "just go" knob. After the user narrates lines into a scene with
// post.line, the transcript ends on their own authored beats and it is plainly the
// character's turn — but nothing asked the model for it. ▶ sends turn.advance with
// NO params: the whole point is that nothing is injected into the transcript.
//
// Zero backend — asserts the button renders beside ✨, sends the bare verb, and
// stays disabled while a turn is in flight.
test('stage: advance runs the next turn with nothing injected', async ({ page }) => {
  await page.route('**/media/**', (route) =>
    route.fulfill({ contentType: 'image/svg+xml', body: '<svg xmlns="http://www.w3.org/2000/svg" width="80" height="80"><rect width="80" height="80" fill="#4a5a6a"/></svg>' }),
  )

  let advanced = 0
  let advanceParams: unknown = 'unset'
  const mock = await installMockBackend(page, {
    respond: (method, params) => {
      if (method === 'cards.list') return { cards: [{ id: 'kobeni-1', name: 'Kobeni', greetings: 1 }] }
      if (method === 'personas.list') return { personas: [] }
      if (method === 'sessions.create') return { session: { id: SMOKE_SESSION, title: 'The Fitting', experience: 'chat', card: 'kobeni-1' } }
      if (method === 'cards.get') return { id: 'kobeni-1', name: 'Kobeni', greetings: 1, raw: {} }
      if (method === 'turn.advance') {
        advanced++
        advanceParams = params
        return {}
      }
      return undefined
    },
  })

  await page.goto('/stage.html')
  await page.locator('.stage-card').first().click()
  await expect(page.locator('.stage-composer')).toBeVisible()
  await mock.subscribed

  // A scene that ends on the user's OWN authored line — the state that makes
  // advance necessary (nothing left to reply to, but it is the character's turn).
  mock.pushEvent(
    {
      type: 'snapshot',
      snapshot: {
        session: { id: SMOKE_SESSION, title: 'The Fitting', experience: 'chat', card: 'kobeni-1' },
        epoch: 1,
        base: 0,
        total: 2,
        messages: [
          { role: 'user', content: [{ type: 'text', text: 'I sign both copies.' }] },
          { role: 'assistant', content: [{ type: 'text', text: '*The fitting passed in a blur.*' }], directed: true },
        ],
        busy: false,
      },
    },
    SMOKE_SESSION,
  )

  const advanceBtn = page.locator('.stage-advance-btn')
  await expect(advanceBtn).toBeVisible()
  await expect(advanceBtn).toBeEnabled()

  await advanceBtn.click()
  await expect.poll(() => advanced).toBe(1)
  // No params by design: advance injects nothing — not a direction, not a message.
  expect(advanceParams == null).toBe(true)

  // The composer went busy on the optimistic click, so ▶ cannot be double-fired
  // into a second turn while the first is still running.
  await expect(advanceBtn).toBeDisabled()

  if (process.env.ADVANCE_SHOT) await page.screenshot({ path: `${process.env.ADVANCE_SHOT}.png`, fullPage: true })
})
