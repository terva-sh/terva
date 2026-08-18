import { test, expect } from '@playwright/test'
import { SMOKE_SESSION, editButtonFor, installStageBackend, stubMedia } from './support'

// Message-scoped variants (Option C): editing an OLDER message (not the last
// response) keeps its alternatives, so a `‹n/m›` swipe control appears on THAT
// message's row — and swiping it posts turn.swipe with an index, which the daemon
// routes to the per-message swipe. This drives the client flow against a mock that
// plays the pre/post-edit snapshots the daemon would send.
test('stage: an edited older message gets its own swipe control', async ({ page }) => {
  await stubMedia(page)

  let edited: { index?: number; text?: string } | null = null
  let swiped: { epoch?: number; index?: number; variant?: number } | null = null
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

  // A two-exchange transcript; the first assistant reply (index 1) is the OLDER
  // message we edit. `mark` carries the message-scoped variant at index 1.
  const snapshot = (opts: { epoch: number; firstReply: string; mark?: { index: number; variants: number; active: number } }) => ({
    type: 'snapshot',
    snapshot: {
      session: { id: SMOKE_SESSION, title: 'Chat', experience: 'chat', card: 'card-1' },
      epoch: opts.epoch,
      base: 0,
      total: 4,
      messages: [
        { role: 'user', content: [{ type: 'text', text: 'hi' }] },
        { role: 'assistant', content: [{ type: 'text', text: opts.firstReply }] },
        { role: 'user', content: [{ type: 'text', text: 'and then?' }] },
        { role: 'assistant', content: [{ type: 'text', text: 'the second reply' }] },
      ],
      busy: false,
      ...(opts.mark ? { variant_marks: [{ ...opts.mark, span: false }] } : {}),
    },
  })

  await page.goto('/stage.html')
  await page.locator('.stage-card').first().click()
  await expect(page.locator('.stage-composer')).toBeVisible()
  await mock.subscribed

  mock.pushEvent(snapshot({ epoch: 1, firstReply: 'the first reply' }), SMOKE_SESSION)
  await expect(page.locator('.stage-bubble', { hasText: 'the first reply' })).toBeVisible()
  await expect(page.locator('.stage-swipe--msg')).toHaveCount(0)

  // Edit the OLDER assistant message (index 1).
  await editButtonFor(page, 'the first reply').click()
  await page.locator('.stage-edit textarea').fill('the first reply, revised')
  await page.locator('.stage-edit button', { hasText: 'Save' }).click()
  await expect.poll(() => edited).toEqual({ epoch: 1, index: 1, text: 'the first reply, revised' })

  // The daemon kept the original as a take: the post-edit snapshot marks index 1,
  // so a swipe control appears on THAT message's row (exactly one, not the tail).
  mock.pushEvent(snapshot({ epoch: 2, firstReply: 'the first reply, revised', mark: { index: 1, variants: 2, active: 1 } }), SMOKE_SESSION)
  await expect(page.locator('.stage-swipe--msg')).toHaveCount(1)
  await expect(page.locator('.stage-swipe--msg span')).toHaveText('2/2')
  if (process.env.MSG_VARIANT_SHOT) await page.screenshot({ path: `${process.env.MSG_VARIANT_SHOT}.png`, fullPage: true })

  // Swipe back to the original — posts turn.swipe WITH an index (the per-message route).
  await page.locator('.stage-swipe--msg button', { hasText: '◀' }).click()
  await expect.poll(() => swiped).toEqual({ epoch: 2, index: 1, variant: 0 })
  mock.pushEvent(snapshot({ epoch: 3, firstReply: 'the first reply', mark: { index: 1, variants: 2, active: 0 } }), SMOKE_SESSION)
  await expect(page.locator('.stage-bubble', { hasText: 'the first reply' })).toBeVisible()
  await expect(page.locator('.stage-swipe--msg span')).toHaveText('1/2')
})
