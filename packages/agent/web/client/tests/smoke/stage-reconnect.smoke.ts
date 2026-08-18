import { test, expect } from '@playwright/test'
import { SMOKE_SESSION, installStageBackend, stubMedia } from './support'

// The reconnect regression, and the reason "I submitted and nothing rendered"
// happened.
//
// Server-side subscriptions are per-connection: ServeConn builds a fresh, empty
// subs map, so every subscription dies with the socket. Stage used to fire
// `subscribe` exactly once, from a useEffect keyed only on (client, sessionId) —
// connection identity was not in the deps. So after ANY reconnect (daemon restart,
// laptop sleep, radio handoff, PWA backgrounding) Stage held a subscription the
// daemon no longer had. The socket was open again, so prompts still went out and
// turns still ran and persisted — but no events ever came back. The user's own
// message never appeared, `busy` never cleared, and only a reload recovered it.
//
// This drops the socket and asserts Stage re-subscribes on the new connection and
// renders events pushed down it. Before the fix the second subscribe never came.
test('stage: re-subscribes after a reconnect and keeps rendering', async ({ page }) => {
  await stubMedia(page)

  const mock = await installStageBackend(page, {
    respond: (method) => {
      if (method === 'cards.list') return { cards: [{ id: 'kobeni-1', name: 'Kobeni', greetings: 1 }] }
      if (method === 'sessions.create') return { session: { id: SMOKE_SESSION, title: 'The Fitting', experience: 'chat', card: 'kobeni-1' } }
      if (method === 'cards.get') return { id: 'kobeni-1', name: 'Kobeni', greetings: 1, raw: {} }
      return undefined
    },
  })

  await page.goto('/stage.html')
  await page.locator('.stage-card').first().click()
  await expect(page.locator('.stage-composer')).toBeVisible()
  await mock.subscribed

  const first = mock.subscribeCount()
  expect(first).toBeGreaterThan(0)

  // The socket dies under us. (It used to say nothing at all about that; the
  // connection banner does now — see boot-state.smoke.ts. This test is still
  // about the invisible half: whether the new socket carries a subscription.)
  mock.drop()

  // The client reconnects on its own; Stage must re-subscribe on the NEW socket.
  await expect.poll(() => mock.subscribeCount(), { timeout: 15_000 }).toBeGreaterThan(first)

  // And the new subscription must actually deliver: push a turn down it and
  // assert BOTH the user's message and the reply render. This is the user-visible
  // symptom — before the fix neither appeared until a page reload.
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
          { role: 'assistant', content: [{ type: 'text', text: 'She takes the pen.' }] },
        ],
        busy: false,
      },
    },
    SMOKE_SESSION,
  )

  await expect(page.locator('.stage-transcript')).toContainText('I sign both copies.')
  await expect(page.locator('.stage-transcript')).toContainText('She takes the pen.')
})

// The other half of the wedge: `busy` used to have exactly ONE clearing path — the
// end-of-turn snapshot. If that snapshot never arrived, busy stayed true forever
// and submit() silently swallowed every later send, leaving the composer stuck on
// "thinking…" with no way out but a reload. A `done` event now clears it too.
test('stage: a done event clears busy even with no snapshot', async ({ page }) => {
  await stubMedia(page)

  const mock = await installStageBackend(page, {
    respond: (method) => {
      if (method === 'cards.list') return { cards: [{ id: 'kobeni-1', name: 'Kobeni', greetings: 1 }] }
      if (method === 'sessions.create') return { session: { id: SMOKE_SESSION, title: 'The Fitting', experience: 'chat', card: 'kobeni-1' } }
      if (method === 'cards.get') return { id: 'kobeni-1', name: 'Kobeni', greetings: 1, raw: {} }
      return undefined
    },
  })

  await page.goto('/stage.html')
  await page.locator('.stage-card').first().click()
  await expect(page.locator('.stage-composer')).toBeVisible()
  await mock.subscribed

  // Send: busy goes true optimistically, so the header shows "thinking…".
  await page.locator('.stage-composer textarea').fill('hello')
  await page.locator('.stage-composer button', { hasText: 'Send' }).click()
  await expect(page.locator('.stage-status')).toHaveText('thinking…')

  // A bare `done` with NO snapshot behind it — the shape a dropped snapshot leaves.
  mock.pushEvent({ type: 'done' }, SMOKE_SESSION)

  await expect(page.locator('.stage-status')).toHaveText('')
})
