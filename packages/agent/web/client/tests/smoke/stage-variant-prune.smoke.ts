import { test, expect } from '@playwright/test'
import { editButtonFor, installMockBackend, SMOKE_SESSION } from './support'

// Variant cleanup (§9): a message with alternatives can be tidied — ✕ on its swipe
// control drops the current take (variants.drop), and "Keep only this" in the edit
// box collapses to the active take and removes the marker (variants.prune). Driven
// against a mock that plays the snapshots the daemon would send.
test('stage: drop a take and prune a message variant', async ({ page }) => {
  await page.route('**/media/**', (route) =>
    route.fulfill({ contentType: 'image/svg+xml', body: '<svg xmlns="http://www.w3.org/2000/svg" width="80" height="80"><rect width="80" height="80" fill="#4a5a6a"/></svg>' }),
  )

  let dropped: { index?: number; variant?: number } | null = null
  let pruned: { index?: number } | null = null
  const mock = await installMockBackend(page, {
    respond: (method, params) => {
      if (method === 'cards.list') return { cards: [{ id: 'card-1', name: 'Ivy', greetings: 1 }] }
      if (method === 'personas.list') return { personas: [] }
      if (method === 'cards.get') return { id: 'card-1', name: 'Ivy', greetings: 1, raw: {} }
      if (method === 'sessions.create') return { session: { id: SMOKE_SESSION, title: 'Chat', experience: 'chat', card: 'card-1' } }
      if (method === 'variants.drop') {
        dropped = params as typeof dropped
        return {}
      }
      if (method === 'variants.prune') {
        pruned = params as typeof pruned
        return {}
      }
      return undefined
    },
  })

  const snapshot = (opts: { epoch: number; reply: string; mark?: { index: number; variants: number; active: number } }) => ({
    type: 'snapshot',
    snapshot: {
      session: { id: SMOKE_SESSION, title: 'Chat', experience: 'chat', card: 'card-1' },
      epoch: opts.epoch,
      base: 0,
      total: 4,
      messages: [
        { role: 'user', content: [{ type: 'text', text: 'hi' }] },
        { role: 'assistant', content: [{ type: 'text', text: opts.reply }] },
        { role: 'user', content: [{ type: 'text', text: 'go on' }] },
        { role: 'assistant', content: [{ type: 'text', text: 'the last reply' }] },
      ],
      busy: false,
      ...(opts.mark ? { variant_marks: [{ ...opts.mark, span: false }] } : {}),
    },
  })

  await page.goto('/stage.html')
  await page.locator('.stage-card').first().click()
  await expect(page.locator('.stage-composer')).toBeVisible()
  await mock.subscribed

  // Message 1 has three takes (active 2). Its swipe control offers ✕.
  mock.pushEvent(snapshot({ epoch: 1, reply: 'take three', mark: { index: 1, variants: 3, active: 2 } }), SMOKE_SESSION)
  await expect(page.locator('.stage-swipe--msg span')).toHaveText('3/3')

  // ✕ drops the current take (active 2).
  await page.locator('.stage-swipe__drop').click()
  await expect.poll(() => dropped).toEqual({ epoch: 1, index: 1, variant: 2 })
  mock.pushEvent(snapshot({ epoch: 1, reply: 'take two', mark: { index: 1, variants: 2, active: 1 } }), SMOKE_SESSION)
  await expect(page.locator('.stage-swipe--msg span')).toHaveText('2/2')

  // Editing the message shows "Keep only this"; clicking prunes to the active take.
  await editButtonFor(page, 'take two').click()
  await page.locator('.stage-edit__prune').click()
  await expect.poll(() => pruned).toEqual({ epoch: 1, index: 1 })
  if (process.env.PRUNE_SHOT) await page.screenshot({ path: `${process.env.PRUNE_SHOT}.png`, fullPage: true })

  // The pruned snapshot carries no marker → the swipe control is gone.
  mock.pushEvent(snapshot({ epoch: 1, reply: 'take two' }), SMOKE_SESSION)
  await expect(page.locator('.stage-swipe--msg')).toHaveCount(0)
})
