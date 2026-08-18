import { test, expect } from '@playwright/test'
import { SMOKE_SESSION, installStageBackend, stubMedia } from './support'

// Start the next scene (SD5): the World tab offers the scene break; the sheet
// drafts a title/recap/cold open (one call, creates nothing), every field is
// editable, and only "Start the scene" commits — carrying the author's edits
// and switching to the new session. Zero backend.
test('stage: the scene break drafts, edits, and commits', async ({ page }) => {
  await stubMedia(page)

  const SESSION = { id: SMOKE_SESSION, title: 'The Lowtown Job', experience: 'chat', card: 'elira-1', world_lore: [] }
  const NEXT = { id: 'scene-2', title: 'The North Road', experience: 'chat', card: 'elira-1', world_lore: [] }
  const calls: Record<string, unknown>[] = []
  const mock = await installStageBackend(page, {
    respond: (method, params) => {
      if (method === 'cards.list') return { cards: [{ id: 'elira-1', name: 'Elira', greetings: 1 }] }
      if (method === 'backgrounds.list') return { backgrounds: [] }
      if (method === 'sessions.create') return { session: SESSION }
      if (method === 'cards.get') return { id: 'elira-1', name: 'Elira', greetings: 1, avatar_url: '/media/cards/elira-1', raw: {} }
      if (method === 'sessions.next_scene') {
        const p = params as Record<string, unknown>
        calls.push(p)
        if (p.commit) return { title: p.title, summary: p.summary, opening: p.opening, session: NEXT }
        return {
          note: 'A clean ending.',
          title: 'The North Road',
          summary: 'They owe Marrow three silver; the search leaves at first light.',
          opening: '*Dawn finds the shop cold.*',
          // 5881b9c4: a draft reports the World the story is in, or suggests a
          // name for one. Absent here, the grouping offer defaults ON with an
          // empty name and the commit button stays disabled — which is what the
          // real daemon avoids by prefilling, and what this mock must too.
          world_name: 'Lowtown',
        }
      }
      return undefined
    },
  })

  await page.goto('/stage.html')
  await page.locator('.stage-card').first().click()
  await expect(page.locator('.stage-composer')).toBeVisible()
  await mock.subscribed
  mock.pushEvent(
    { type: 'snapshot', snapshot: { session: SESSION, epoch: 1, base: 0, total: 0, messages: [], busy: false } },
    SMOKE_SESSION,
  )

  // The World tab carries the entry point; opening it closes the drawer.
  await page.locator('.stage-steer-btn').click()
  await page.locator('.stage-drawer__tab', { hasText: 'World' }).click()
  await page.locator('.stage-nextscene__open').click()

  // The sheet drafts on open — one call, and it created nothing (no commit).
  const sheet = page.locator('.stage-nextscene')
  await expect(sheet).toBeVisible()
  // The title input specifically: the sheet gained a World-grouping checkbox
  // and a world-name field in 5881b9c4, so a bare `input` is three elements.
  const titleInput = sheet.locator('.stage-nextscene__field input')
  await expect(titleInput).toHaveValue('The North Road')
  // The World-grouping offer: on by default, prefilled with the daemon's
  // suggestion, because two scenes with nothing tying them together is the one
  // case worth asking about.
  await expect(sheet.locator('.stage-nextscene__group input')).toBeChecked()
  await expect(sheet.locator('.stage-nextscene__worldname')).toHaveValue('Lowtown')
  await expect(sheet.locator('textarea').first()).toHaveValue(/three silver/)
  await expect(sheet.locator('textarea').nth(1)).toHaveValue('*Dawn finds the shop cold.*')
  expect(calls).toHaveLength(1)
  expect(calls[0].commit).toBeFalsy()

  // Every field is the author's to edit; the commit carries the edits, not the
  // draft.
  const subsBefore = mock.subscribeCount()
  await titleInput.fill('First Light')
  await sheet.locator('textarea').nth(1).fill('*The bell wakes her before the sun does.*')
  await sheet.locator('.stage-nextscene__go').click()

  await expect.poll(() => calls.length).toBe(2)
  expect(calls[1]).toMatchObject({
    commit: true,
    title: 'First Light',
    opening: '*The bell wakes her before the sun does.*',
  })
  expect(calls[1].summary).toContain('three silver')
  // The commit carries the World to promote, so the two scenes land grouped.
  expect(calls[1].world).toBe('Lowtown')
  // Committing closes the sheet and switches to the new scene — proven by the
  // client applying an event addressed to the NEW session id, which it only
  // does once subscribed to it. Wait for that subscribe: an event pushed to a
  // session the client has not selected yet is dropped as off-session, and
  // under a loaded suite the push otherwise beats the switch.
  await expect(sheet).toHaveCount(0)
  await expect.poll(() => mock.subscribeCount()).toBeGreaterThan(subsBefore)
  mock.pushEvent(
    {
      type: 'snapshot',
      snapshot: {
        session: NEXT,
        epoch: 1,
        base: 0,
        total: 1,
        messages: [{ role: 'assistant', content: [{ type: 'text', text: '*Dawn finds the shop cold.*' }], directed: true, actor: 'Elira' }],
        busy: false,
      },
    },
    NEXT.id,
  )
  await expect(page.locator('.stage-chat__title')).toHaveText('The North Road')
  await expect(page.locator('.stage-bubble')).toContainText('Dawn finds the shop cold.')
})

// The doctor's scene_break kind has no verb of its own: accepting it hands off
// to the same sheet, seeded with the proposed title.
test('stage: a doctor scene_break proposal opens the next-scene sheet', async ({ page }) => {
  await stubMedia(page)

  const SESSION = { id: SMOKE_SESSION, title: 'The Lowtown Job', experience: 'chat', card: 'elira-1', world_lore: [] }
  let drafted = false
  const mock = await installStageBackend(page, {
    respond: (method, params) => {
      if (method === 'cards.list') return { cards: [{ id: 'elira-1', name: 'Elira', greetings: 1 }] }
      if (method === 'backgrounds.list') return { backgrounds: [] }
      if (method === 'sessions.create') return { session: SESSION }
      if (method === 'cards.get') return { id: 'elira-1', name: 'Elira', greetings: 1, avatar_url: '/media/cards/elira-1', raw: {} }
      if (method === 'sessions.doctor') {
        return {
          proposals: [
            { id: 'p1', kind: 'scene_break', rationale: 'they part at the door', name: 'The North Road', content: 'The night ends; the next scene opens at first light.' },
          ],
        }
      }
      if (method === 'sessions.next_scene') {
        const p = params as Record<string, unknown>
        if (!p.commit) {
          drafted = true
          return { title: 'The North Road', summary: 'They parted at the door.', opening: '*Dawn.*' }
        }
        return { session: SESSION }
      }
      return undefined
    },
  })

  await page.goto('/stage.html')
  await page.locator('.stage-card').first().click()
  await expect(page.locator('.stage-composer')).toBeVisible()
  await mock.subscribed
  mock.pushEvent(
    { type: 'snapshot', snapshot: { session: SESSION, epoch: 1, base: 0, total: 0, messages: [], busy: false } },
    SMOKE_SESSION,
  )

  await page.locator('.stage-steer-btn').click()
  await page.locator('.stage-drawer__tab', { hasText: 'World' }).click()
  await page.locator('.stage-doctor__run').click()

  const proposal = page.locator('.stage-doctor__item')
  await expect(proposal.locator('.stage-doctor__kind')).toContainText('scene break')
  await expect(proposal.locator('.stage-doctor__scope')).toContainText('opens a new scene')
  // Its accept is a hand-off, not an apply — no world.lore.put, no "applied".
  await proposal.locator('.stage-doctor__accept').click()
  await expect(page.locator('.stage-nextscene')).toBeVisible()
  await expect.poll(() => drafted).toBe(true)
})
