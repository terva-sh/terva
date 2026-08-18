import { test, expect } from '@playwright/test'
import { SMOKE_SESSION, editButtonFor, installStageBackend, stubMedia } from './support'

// Fork-from-here (§8): editing a message with a long downstream offers "Branch
// here" — a new session that shares the transcript through that message and
// diverges, leaving this one intact. This drives the client flow: the deep-edit
// note appears, Branch posts sessions.fork {from_index}, and the app switches to
// the returned session.
test('stage: a deep edit offers Branch here, which forks and navigates', async ({ page }) => {
  await stubMedia(page)

  let forked: { from_index?: number } | null = null
  const mock = await installStageBackend(page, {
    respond: (method, params) => {
      if (method === 'cards.get') return { id: 'card-1', name: 'Ivy', greetings: 1, raw: {} }
      if (method === 'sessions.create') return { session: { id: SMOKE_SESSION, title: 'Chat', experience: 'chat', card: 'card-1' } }
      if (method === 'sessions.fork') {
        forked = params as typeof forked
        return { session: { id: 'branch-1', title: 'Branch', experience: 'chat', card: 'card-1' } }
      }
      return undefined
    },
  })

  await page.goto('/stage.html')
  await page.locator('.stage-card').first().click()
  await expect(page.locator('.stage-composer')).toBeVisible()
  await mock.subscribed

  // A transcript deep enough that editing the first reply (index 1) has a large
  // downstream (4 messages below → past BRANCH_HINT).
  mock.pushEvent(
    {
      type: 'snapshot',
      snapshot: {
        session: { id: SMOKE_SESSION, title: 'Chat', experience: 'chat', card: 'card-1' },
        epoch: 1,
        base: 0,
        total: 6,
        messages: [
          { role: 'user', content: [{ type: 'text', text: 'hi' }] },
          { role: 'assistant', content: [{ type: 'text', text: 'the first reply' }] },
          { role: 'user', content: [{ type: 'text', text: 'go on' }] },
          { role: 'assistant', content: [{ type: 'text', text: 'the second reply' }] },
          { role: 'user', content: [{ type: 'text', text: 'and more' }] },
          { role: 'assistant', content: [{ type: 'text', text: 'the third reply' }] },
        ],
        busy: false,
      },
    },
    SMOKE_SESSION,
  )
  await expect(page.locator('.stage-bubble', { hasText: 'the first reply' })).toBeVisible()

  // Editing that deep message surfaces the branch note and the Branch action.
  await editButtonFor(page, 'the first reply').click()
  await expect(page.locator('.stage-edit__note')).toContainText('Branch starts a new thread')
  if (process.env.FORK_SHOT) await page.screenshot({ path: `${process.env.FORK_SHOT}.png`, fullPage: true })
  await page.locator('.stage-edit__branch').click()

  // Branch forks at the edited message's index and the app navigates to the child.
  await expect.poll(() => forked).toEqual({ from_index: 1 })
  const branchSnapshot = {
    type: 'snapshot',
    snapshot: {
      session: { id: 'branch-1', title: 'Branch', experience: 'chat', card: 'card-1' },
      epoch: 1,
      base: 0,
      total: 3,
      messages: [
        { role: 'user', content: [{ type: 'text', text: 'hi' }] },
        { role: 'assistant', content: [{ type: 'text', text: 'the first reply' }] },
        { role: 'assistant', content: [{ type: 'text', text: 'now in the new branch' }] },
      ],
      busy: false,
    },
  }
  // Re-push each poll iteration so it lands once the remounted chat has subscribed
  // to the new session (the switch is async).
  await expect
    .poll(async () => {
      mock.pushEvent(branchSnapshot, 'branch-1')
      return page.locator('.stage-bubble', { hasText: 'now in the new branch' }).count()
    })
    .toBeGreaterThan(0)
})
