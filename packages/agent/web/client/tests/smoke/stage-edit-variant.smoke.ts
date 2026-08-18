import { test, expect } from '@playwright/test'
import { SMOKE_SESSION, editButtonFor, installStageBackend, stubMedia } from './support'

// Edit-as-variant (inline-editing MVP): editing the last response is
// non-destructive — the daemon keeps the original as a swipeable take, so the
// tail's swipe counter lights up and a swipe restores the original. This smoke
// drives the real client flow (edit the bubble → Save → counter → swipe) against
// a mock backend that plays the pre/post-edit snapshots the daemon would send.
test('stage: editing the last response makes it a swipeable variant', async ({ page }) => {
  await stubMedia(page)

  let edited: { epoch?: number; index?: number; text?: string } | null = null
  let swiped: { epoch?: number; variant?: number } | null = null
  const mock = await installStageBackend(page, {
    respond: (method, params) => {
      if (method === 'cards.get') return { id: 'card-1', name: 'Ivy', greetings: 1, raw: {} }
      if (method === 'sessions.create') return { session: { id: SMOKE_SESSION, title: 'Chat', experience: 'chat', card: 'card-1' } }
      if (method === 'message.edit') {
        edited = params as typeof edited
        return {}
      }
      if (method === 'turn.swipe') {
        swiped = params as typeof swiped
        return {}
      }
      return undefined
    },
  })

  const snapshot = (opts: { epoch: number; assistant: string; tail?: { span_start: number; variants: number; active: number } }) => ({
    type: 'snapshot',
    snapshot: {
      session: { id: SMOKE_SESSION, title: 'Chat', experience: 'chat', card: 'card-1' },
      epoch: opts.epoch,
      base: 0,
      total: 2,
      messages: [
        { role: 'user', content: [{ type: 'text', text: 'hello' }] },
        { role: 'assistant', content: [{ type: 'text', text: opts.assistant }] },
      ],
      busy: false,
      ...(opts.tail ? { tail: opts.tail } : {}),
    },
  })

  await page.goto('/stage.html')
  await page.locator('.stage-card').first().click()
  await expect(page.locator('.stage-composer')).toBeVisible()
  await mock.subscribed

  // A single completed response — no swipe counter yet.
  mock.pushEvent(snapshot({ epoch: 1, assistant: 'first take' }), SMOKE_SESSION)
  await expect(page.locator('.stage-bubble', { hasText: 'first take' })).toBeVisible()
  await expect(page.locator('.stage-swipe')).toHaveCount(0)

  // Edit the response: press ✎, rewrite, Save → posts message.edit.
  await editButtonFor(page, 'first take').click()
  await page.locator('.stage-edit textarea').fill('edited take')
  await page.locator('.stage-edit button', { hasText: 'Save' }).click()
  await expect.poll(() => edited).toEqual({ epoch: 1, index: 1, text: 'edited take' })

  // The daemon kept the original as a take: the post-edit snapshot carries a
  // 2-variant tail with the edit active, and the swipe counter appears.
  mock.pushEvent(snapshot({ epoch: 2, assistant: 'edited take', tail: { span_start: 1, variants: 2, active: 1 } }), SMOKE_SESSION)
  await expect(page.locator('.stage-bubble', { hasText: 'edited take' })).toBeVisible()
  await expect(page.locator('.stage-swipe')).toBeVisible()
  await expect(page.locator('.stage-swipe span')).toHaveText('2/2')
  if (process.env.EDIT_VARIANT_SHOT) await page.screenshot({ path: `${process.env.EDIT_VARIANT_SHOT}.png`, fullPage: true })

  // Swipe back to the original take.
  await page.locator('.stage-swipe button', { hasText: '◀' }).click()
  await expect.poll(() => swiped).toEqual({ epoch: 2, variant: 0 })
  mock.pushEvent(snapshot({ epoch: 3, assistant: 'first take', tail: { span_start: 1, variants: 2, active: 0 } }), SMOKE_SESSION)
  await expect(page.locator('.stage-bubble', { hasText: 'first take' })).toBeVisible()
  await expect(page.locator('.stage-swipe span')).toHaveText('1/2')
})
