import { test, expect } from '@playwright/test'
import { SMOKE_SESSION, installStageBackend, stubMedia } from './support'

// Stage routed every event except `snapshot` and `session_updated` into the
// transcript folder, where unknown types hit a silent no-op — so
// `permission_request` and `ask_request` were DROPPED. A turn that reached for a
// gated tool, or asked a question, parked on the daemon forever with nothing on
// screen and no way to answer. From the user's side that is indistinguishable
// from "I sent a message and nothing happened".
//
// These assert the prompt renders and its answer goes back on the wire.
test('stage: a tool approval renders and can be answered', async ({ page }) => {
  await stubMedia(page)

  let approved: unknown = null
  const mock = await installStageBackend(page, {
    respond: (method, params) => {
      if (method === 'cards.list') return { cards: [{ id: 'kobeni-1', name: 'Kobeni', greetings: 1 }] }
      if (method === 'sessions.create') return { session: { id: SMOKE_SESSION, title: 'The Fitting', experience: 'chat', card: 'kobeni-1' } }
      if (method === 'cards.get') return { id: 'kobeni-1', name: 'Kobeni', greetings: 1, raw: {} }
      if (method === 'approve') {
        approved = params
        return {}
      }
      return undefined
    },
  })

  await page.goto('/stage.html')
  await page.locator('.stage-card').first().click()
  await expect(page.locator('.stage-composer')).toBeVisible()
  await mock.subscribed

  mock.pushEvent(
    { type: 'permission_request', permission: { call_id: 'call-1', tool: 'read_file', preview: '/etc/hosts' } },
    SMOKE_SESSION,
  )

  const prompt = page.locator('.stage-interact')
  await expect(prompt).toBeVisible()
  await expect(prompt).toContainText('read_file')
  await expect(prompt).toContainText('/etc/hosts')

  await prompt.locator('button', { hasText: 'Allow' }).first().click()
  await expect.poll(() => approved).toEqual({ call_id: 'call-1', decision: { allow: true } })
  // Dismissed locally on answer — the user should not have to wait for the
  // daemon's resolved event to see it go.
  await expect(prompt).toHaveCount(0)
})

test('stage: an ask renders its options and answers', async ({ page }) => {
  await stubMedia(page)

  let answered: unknown = null
  const mock = await installStageBackend(page, {
    respond: (method, params) => {
      if (method === 'cards.list') return { cards: [{ id: 'kobeni-1', name: 'Kobeni', greetings: 1 }] }
      if (method === 'sessions.create') return { session: { id: SMOKE_SESSION, title: 'The Fitting', experience: 'chat', card: 'kobeni-1' } }
      if (method === 'cards.get') return { id: 'kobeni-1', name: 'Kobeni', greetings: 1, raw: {} }
      if (method === 'answer') {
        answered = params
        return {}
      }
      return undefined
    },
  })

  await page.goto('/stage.html')
  await page.locator('.stage-card').first().click()
  await expect(page.locator('.stage-composer')).toBeVisible()
  await mock.subscribed

  mock.pushEvent(
    { type: 'ask_request', ask: { ask_id: 'ask-1', question: 'Which door?', options: ['left', 'right'] } },
    SMOKE_SESSION,
  )

  const prompt = page.locator('.stage-interact')
  await expect(prompt).toContainText('Which door?')
  await prompt.locator('button', { hasText: 'right' }).click()
  await expect.poll(() => answered).toEqual({ ask_id: 'ask-1', answer: { answer: 'right' } })
})

// The self-heal that matters after the reconnect fix: a snapshot carries anything
// still pending, so a prompt whose event was missed comes back on re-subscribe
// rather than stranding the turn.
test('stage: a pending approval is recovered from the snapshot', async ({ page }) => {
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

  // No permission_request event — only a snapshot that says one is outstanding.
  mock.pushEvent(
    {
      type: 'snapshot',
      snapshot: {
        session: { id: SMOKE_SESSION, title: 'The Fitting', experience: 'chat', card: 'kobeni-1' },
        epoch: 1,
        base: 0,
        total: 0,
        messages: [],
        busy: true,
        permissions: [{ call_id: 'call-9', tool: 'write_file' }],
      },
    },
    SMOKE_SESSION,
  )

  await expect(page.locator('.stage-interact')).toContainText('write_file')
})
