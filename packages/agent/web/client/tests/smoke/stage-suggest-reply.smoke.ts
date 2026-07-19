import { test, expect } from '@playwright/test'
import { installMockBackend, SMOKE_SESSION } from './support'

// Suggest a reply (S9): the agentic composer aid. From the chat composer, ✨
// opens a modal where the user sketches an intent, the model drafts the player's
// next line, and the user refines it in a back-and-forth before copying it into
// the composer (they still send it). This smoke drives the real client flow
// (sketch → Draft → Refine → Use this) against a mock that echoes the guidance
// and reports how much history it was sent — proving the refinement is threaded
// statelessly and the final draft lands in the composer.
test('stage: suggest a reply — sketch, refine, and use it', async ({ page }) => {
  await page.route('**/media/**', (route) =>
    route.fulfill({ contentType: 'image/svg+xml', body: '<svg xmlns="http://www.w3.org/2000/svg" width="80" height="80"><rect width="80" height="80" fill="#4a5a6a"/></svg>' }),
  )

  const calls: Array<{ history: unknown[]; note: string; sess?: string }> = []
  const mock = await installMockBackend(page, {
    respond: (method, params, sess) => {
      if (method === 'cards.list') return { cards: [{ id: 'card-1', name: 'Ivy', greetings: 1 }] }
      if (method === 'personas.list') return { personas: [] }
      if (method === 'cards.get') return { id: 'card-1', name: 'Ivy', greetings: 1, raw: {} }
      if (method === 'sessions.create') return { session: { id: SMOKE_SESSION, title: 'Chat', experience: 'chat', card: 'card-1' } }
      if (method === 'suggest.reply') {
        const p = params as { history?: unknown[]; note?: string }
        calls.push({ history: p.history ?? [], note: p.note ?? '', sess })
        return { draft: `DRAFT(${p.note || 'cold'})#${(p.history ?? []).length}` }
      }
      return undefined
    },
  })

  await page.goto('/stage.html')
  await page.locator('.stage-card').first().click()
  await expect(page.locator('.stage-composer')).toBeVisible()
  await mock.subscribed

  // Establish a scene so the chat isn't empty (the greeting the character opened with).
  mock.pushEvent(
    {
      type: 'snapshot',
      snapshot: {
        session: { id: SMOKE_SESSION, title: 'Chat', experience: 'chat', card: 'card-1' },
        epoch: 1,
        base: 0,
        total: 1,
        messages: [{ role: 'assistant', content: [{ type: 'text', text: 'The blade rests on the anvil.' }] }],
        busy: false,
      },
    },
    SMOKE_SESSION,
  )
  await expect(page.locator('.stage-bubble', { hasText: 'The blade rests' })).toBeVisible()

  // Open the ✨ modal → the sketch view.
  await page.locator('.stage-suggest-btn').click()
  await expect(page.locator('.stage-suggest')).toBeVisible()
  await expect(page.locator('.stage-suggest__hint')).toBeVisible()

  // Sketch an intent and draft it.
  await page.locator('.stage-suggest__note').fill('ask about the sword')
  await page.locator('.stage-suggest__go').click()

  const draft = page.locator('.stage-suggest__draft')
  await expect(draft).toHaveValue('DRAFT(ask about the sword)#0')
  // The first call carried no history and the session address in the frame.
  await expect.poll(() => calls.length).toBe(1)
  expect(calls[0].history).toEqual([])
  expect(calls[0].note).toBe('ask about the sword')
  expect(calls[0].sess).toBe(SMOKE_SESSION)
  // The guidance is shown above its draft in the thread.
  await expect(page.locator('.stage-suggest__guide')).toContainText('ask about the sword')

  // Refine it — the refinement threads the prior round back to the daemon.
  await page.locator('.stage-suggest__note').fill('shorter')
  await page.locator('.stage-suggest__refine-go').click()

  await expect(draft).toHaveValue('DRAFT(shorter)#1')
  if (process.env.SUGGEST_SHOT) await page.screenshot({ path: `${process.env.SUGGEST_SHOT}.png`, fullPage: true })
  await expect.poll(() => calls.length).toBe(2)
  expect((calls[1].history as unknown[]).length).toBe(1) // the first (note→draft) round
  expect(calls[1].note).toBe('shorter')

  // Use this → the draft lands in the composer (not auto-sent).
  await page.locator('.stage-suggest__use').click()
  await expect(page.locator('.stage-suggest')).toHaveCount(0)
  await expect(page.locator('.stage-composer textarea')).toHaveValue('DRAFT(shorter)#1')
})

test('stage: suggest a reply — a cold suggestion needs no sketch', async ({ page }) => {
  const mock = await installMockBackend(page, {
    respond: (method, params) => {
      if (method === 'cards.list') return { cards: [{ id: 'card-1', name: 'Ivy', greetings: 1 }] }
      if (method === 'personas.list') return { personas: [] }
      if (method === 'cards.get') return { id: 'card-1', name: 'Ivy', greetings: 1, raw: {} }
      if (method === 'sessions.create') return { session: { id: SMOKE_SESSION, title: 'Chat', experience: 'chat', card: 'card-1' } }
      if (method === 'suggest.reply') {
        const p = params as { note?: string }
        return { draft: `COLD(${p.note || 'none'})` }
      }
      return undefined
    },
  })

  await page.goto('/stage.html')
  await page.locator('.stage-card').first().click()
  await expect(page.locator('.stage-composer')).toBeVisible()
  await mock.subscribed

  await page.locator('.stage-suggest-btn').click()
  // Draft with an empty sketch — a cold suggestion.
  await page.locator('.stage-suggest__go').click()
  await expect(page.locator('.stage-suggest__draft')).toHaveValue('COLD(none)')
  await expect(page.locator('.stage-suggest__guide--cold')).toContainText('cold suggestion')
})

test('stage: the suggest sketch box grows with content', async ({ page }) => {
  await installMockBackend(page, {
    respond: (method) => {
      if (method === 'cards.list') return { cards: [{ id: 'card-1', name: 'Ivy', greetings: 1 }] }
      if (method === 'personas.list') return { personas: [] }
      if (method === 'cards.get') return { id: 'card-1', name: 'Ivy', greetings: 1, raw: {} }
      if (method === 'sessions.create') return { session: { id: SMOKE_SESSION, title: 'Chat', experience: 'chat', card: 'card-1' } }
      return undefined
    },
  })

  await page.goto('/stage.html')
  await page.locator('.stage-card').first().click()
  await expect(page.locator('.stage-composer')).toBeVisible()

  await page.locator('.stage-suggest-btn').click()
  const note = page.locator('.stage-suggest__note')
  const height = async () => (await note.boundingBox())!.height
  const empty = await height()
  await note.fill('line one\nline two\nline three\nline four\nline five')
  expect(await height()).toBeGreaterThan(empty + 20)
})
