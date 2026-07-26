import { test, expect } from '@playwright/test'
import { installMockBackend, SMOKE_SESSION } from './support'

// The editor (Worlds W4): ✏️ on a World-tab character runs cards.doctor in
// session mode (the Toimittaja persona, grounded in THIS scene), proposals
// render for accept/decline, and Apply merges the accepted fields into the
// card via cards.get → cards.edit. Zero backend.
test('stage: enrich a character from the scene via the World tab', async ({ page }) => {
  await page.route('**/media/**', (route) =>
    route.fulfill({ contentType: 'image/svg+xml', body: '<svg xmlns="http://www.w3.org/2000/svg" width="80" height="80"><rect width="80" height="80" fill="#5a7a6a"/></svg>' }),
  )

  const SESSION = {
    id: SMOKE_SESSION,
    title: 'The Lowtown Job',
    experience: 'chat',
    card: 'elira-1',
    cast: { Rook: 'rook-1' },
  }
  const RAW = { name: 'Elira', description: 'A fence.', personality: '' }
  let doctorCall: Record<string, unknown> | null = null
  let edited: Record<string, unknown> | null = null
  const mock = await installMockBackend(page, {
    respond: (method, params) => {
      if (method === 'cards.list') return { cards: [{ id: 'elira-1', name: 'Elira', greetings: 1 }] }
      if (method === 'personas.list') return { personas: [] }
      if (method === 'sessions.create') return { session: SESSION }
      if (method === 'cards.get') return { id: 'elira-1', name: 'Elira', greetings: 1, avatar_url: '/media/cards/elira-1', raw: RAW }
      if (method === 'backgrounds.list') return { backgrounds: [] }
      if (method === 'cards.doctor') {
        doctorCall = params as Record<string, unknown>
        return {
          note: 'The scene gave Elira a real edge.',
          proposals: [
            { id: 'p1', field: 'personality', severity: 'suggestion', rationale: 'Her clipped defiance in the ledger scene.', before: '', after: 'Wry, clipped, never volunteers a name.' },
            { id: 'p2', field: 'description', severity: 'suggestion', rationale: 'The guild debt surfaced on stage.', before: 'A fence.', after: 'A fence who owes the guild a life.' },
          ],
        }
      }
      if (method === 'cards.edit') {
        edited = params as Record<string, unknown>
        return { id: 'elira-1', name: 'Elira', greetings: 1, raw: RAW }
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

  // The bound character leads the On-stage list with an ✏️; the roster member
  // has one too.
  const bound = page.locator('.stage-cast__member', { hasText: 'main character' })
  await expect(bound).toContainText('Elira')
  await bound.locator('button[title*="Enrich"]').click()

  // ✏️ EXPANDS the panel; it does not run. It used to fire cards.doctor on the
  // click, which meant the run happened before the model picker was even on
  // screen and every read went to the default model whatever you picked
  // afterwards. The run is its own button now, below the picker.
  await expect(page.locator('.stage-enrich__start')).toBeVisible()
  expect(doctorCall, 'opening the editor must not run it').toBeNull()
  await page.locator('.stage-enrich__start button').click()

  // The editor ran against THIS session.
  await expect.poll(() => doctorCall).not.toBeNull()
  expect(doctorCall).toMatchObject({ id: 'elira-1', session: SMOKE_SESSION })

  // Proposals render; accept both and apply.
  await expect(page.locator('.stage-enrich__proposal')).toHaveCount(2)
  await expect(page.locator('.stage-enrich')).toContainText('The scene gave Elira a real edge.')
  const proposals = page.locator('.stage-enrich__proposal')
  await proposals.nth(0).locator('button[title="Accept this edit"]').click()
  await proposals.nth(1).locator('button[title="Accept this edit"]').click()
  await page.locator('.stage-enrich__actions .stage-worldlore-form__save').click()

  // Apply merged the accepted fields into the card document wholesale.
  await expect.poll(() => edited).not.toBeNull()
  expect(edited).toEqual({
    id: 'elira-1',
    card: { name: 'Elira', description: 'A fence who owes the guild a life.', personality: 'Wry, clipped, never volunteers a name.' },
  })
  // The sheet closes after a successful apply.
  await expect(page.locator('.stage-enrich')).toHaveCount(0)
})
