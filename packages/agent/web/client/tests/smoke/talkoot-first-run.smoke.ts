import { test, expect } from '@playwright/test'
import { installMockBackend, panelSessionURL } from './support'

// The backend fixture spends nothing. Native turn-to-room delivery has Go
// integration tests; this checks the shipped browser renders that room data.
test('a first team shows replies and can persist a team-only budget waiver', async ({ page }) => {
  await page.setViewportSize({ width: 900, height: 620 })
  await page.addInitScript(() => {
    localStorage.setItem('terva_talkoot_open', '1')
    localStorage.setItem('terva_talkoot_person', 'Drew')
    localStorage.setItem('terva_talkoot_motion', 'off')
  })
  let waived = false
  const updates: unknown[] = []
  const modelUpdates: unknown[] = []
  const crew = () => ({
    id: 'crew', name: 'Coding team', home: '/w', budget_usd_per_day: 20, team_budget_waived: waived,
    members: [
      { id: 'mieli', role: 'coordinator', driver: 'native', tier: 'strong', resolved_provider: 'openai', resolved_model: 'gpt-live', model_source: 'session', posture: 'plan', session: 'lead', mark: { shape: 'hexagon', color: '#3E63DD' }, status: { member: 'mieli', presence: 'idle', spend_usd: 0.04 } },
      { id: 'developer', role: 'specialist', driver: 'native', tier: 'strong', resolved_provider: 'anthropic', resolved_model: 'claude-tier', model_source: 'tier', posture: 'auto-edit', mark: { shape: 'circle', color: '#46A758' }, status: { member: 'developer', presence: 'idle' } },
    ],
  })
  const backend = await installMockBackend(page, {
    groups: ['conversation', 'session', 'control', 'talkoot'],
    respond: (method, params) => {
      switch (method) {
        case 'talkoot.list': return { talkoots: [{ id: 'crew', name: 'Coding team', running: true }] }
        case 'talkoot.get': return crew()
        case 'talkoot.inbox': return { cards: [] }
        case 'talkoot.room': return { total: 2, lines: [
          { type: 'envelope', at: '2026-09-30T20:00:00Z', envelope: { id: 'post', from: 'human:Drew', to: ['mieli'], kind: 'message', body: 'What is the status?', at: '2026-09-30T20:00:00Z', chain: { root: 'post', hops: 0 } } },
          { type: 'envelope', at: '2026-09-30T20:00:01Z', envelope: { id: 'reply', from: 'mieli', to: ['human:Drew'], kind: 'message', body: 'Ready to work. The team can see this reply.', at: '2026-09-30T20:00:01Z', chain: { root: 'post', hops: 1 } } },
        ] }
        case 'models.list': return { models: [
          { id: 'gpt-catalog', provider: 'openai', name: 'Catalog model' },
          { id: 'hidden-model', provider: 'openai', hidden: true },
        ] }
        case 'talkoot.update': {
          const p = params as { team_budget_waived?: boolean; ops?: unknown[] }
          if (p.ops) modelUpdates.push(params)
          else {
            updates.push(params)
            waived = p.team_budget_waived ?? false
          }
          return crew()
        }
      }
      return undefined
    },
  })
  await page.goto(panelSessionURL)
  await expect(page.getByText('Ready to work. The team can see this reply.')).toBeVisible()
  const rows = page.locator('.talkoot-member-row')
  await expect(rows.nth(0).locator('.talkoot-member-model')).toHaveText('openai / gpt-live (live session)')
  await expect(rows.nth(1).locator('.talkoot-member-model')).toHaveText('anthropic / claude-tier')
  await expect(page.getByText('Team daily limit: $20.00')).toBeVisible()
  await page.getByRole('button', { name: 'Waive team daily limit' }).click()
  await expect.poll(() => updates).toEqual([{ id: 'crew', by: 'Drew', team_budget_waived: true }])
  // A real daemon's roster event triggers the view refresh.
  backend.pushEvent({ type: 'talkoot_roster', talkoot: { id: 'crew' } }, '#talkoot:crew')
  await expect(page.getByText('Team daily limit waived')).toBeVisible()
  await page.reload()
  await expect(page.getByText('Team daily limit waived')).toBeVisible()
  const restore = page.getByRole('button', { name: 'Restore team daily limit' })
  await restore.scrollIntoViewIfNeeded()
  await expect(restore).toBeInViewport()
  await restore.click()
  await expect.poll(() => waived).toBe(false)
  backend.pushEvent({ type: 'talkoot_roster', talkoot: { id: 'crew' } }, '#talkoot:crew')
  await expect(page.getByText('Team daily limit: $20.00')).toBeVisible()

  await rows.nth(1).locator('button.talkoot-member').click()
  await page.getByRole('button', { name: 'Member card', exact: true }).click()
  const card = page.getByRole('region', { name: 'Member card' })
  const model = card.getByRole('combobox', { name: 'Model', exact: true })
  await expect(model).toHaveCount(1)
  await expect.poll(() => model.evaluate((el) => Array.from((el as HTMLSelectElement).options, (o) => o.value))).toContain('gpt-catalog')
  expect(await model.evaluate((el) => Array.from((el as HTMLSelectElement).options, (o) => o.value))).not.toContain('hidden-model')
  await model.selectOption('gpt-catalog')
  const save = card.getByRole('button', { name: 'Save changes' })
  await save.scrollIntoViewIfNeeded()
  await expect(save).toBeVisible()
  await save.click()
  await expect.poll(() => modelUpdates).toEqual([
    { id: 'crew', by: 'Drew', ops: [{ op: 'edit', member: 'developer', set: { model: 'gpt-catalog', tier: null } }] },
  ])
})
