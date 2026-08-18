import { test, expect } from '@playwright/test'
import { SMOKE_SESSION, installStageBackend, stubMedia } from './support'

// The session doctor (SD1): the World tab carries Dramaturgi's button; a run
// renders typed proposals; accepting a lore entry posts world.lore.put;
// accepting a promotion imports the seeded card and THEN puts it on stage;
// declining with a reason rides the next round as a decision. Zero backend.
test('stage: the session doctor proposes, applies, and negotiates', async ({ page }) => {
  await stubMedia(page)

  const SESSION = { id: SMOKE_SESSION, title: 'The Lowtown Job', experience: 'chat', card: 'elira-1', world_lore: [] }
  const doctorCalls: Record<string, unknown>[] = []
  const puts: Record<string, unknown>[] = []
  const imports: Record<string, unknown>[] = []
  const castAdds: Record<string, unknown>[] = []
  const mock = await installStageBackend(page, {
    cards: [{ id: 'elira-1', name: 'Elira', greetings: 1 }],
    respond: (method, params) => {
      if (method === 'backgrounds.list') return { backgrounds: [] }
      if (method === 'sessions.create') return { session: SESSION }
      if (method === 'cards.get') return { id: 'elira-1', name: 'Elira', greetings: 1, avatar_url: '/media/cards/elira-1', raw: {} }
      if (method === 'sessions.doctor') {
        doctorCalls.push(params as Record<string, unknown>)
        if (doctorCalls.length === 1) {
          return {
            note: 'Two keepers and a lamplighter.',
            proposals: [
              { id: 'p1', kind: 'lore_entry', rationale: 'the contract scene', name: 'The wage ladder', content: '8s, then 10 after trial.', keys: ['wage'] },
              { id: 'p2', kind: 'open_thread', rationale: 'promised at dusk', name: 'The lieutenant at first light', content: 'The watch arrives at dawn.', keys: ['dawn'], audience: ['Elira'] },
              { id: 'p3', kind: 'cast_promotion', rationale: 'voiced twice, specced in prose', character: 'Marrow', description: 'The lamplighter who keeps the keys.', personality: 'grave, punctual', first_mes: '*He lifts the pole.*' },
            ],
          }
        }
        return { note: 'Revised.', proposals: [] }
      }
      if (method === 'world.lore.put') {
        puts.push(params as Record<string, unknown>)
        return {}
      }
      if (method === 'cards.import') {
        imports.push(params as Record<string, unknown>)
        return { id: 'marrow-9', name: 'Marrow', greetings: 1 }
      }
      if (method === 'cast.add') {
        castAdds.push(params as Record<string, unknown>)
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
    { type: 'snapshot', snapshot: { session: SESSION, epoch: 1, base: 0, total: 0, messages: [], busy: false } },
    SMOKE_SESSION,
  )

  // The doctor lives on the World tab.
  await page.locator('.stage-steer-btn').click()
  await page.locator('.stage-drawer__tab', { hasText: 'World' }).click()
  await page.locator('.stage-doctor__run').click()

  // Three typed proposals render: lore (shared), thread (scoped), promotion.
  await expect(page.locator('.stage-doctor__item')).toHaveCount(3)
  const lore = page.locator('.stage-doctor__item', { hasText: 'The wage ladder' })
  await expect(lore.locator('.stage-doctor__scope')).toHaveText('shared')
  const thread = page.locator('.stage-doctor__item', { hasText: 'The lieutenant at first light' })
  await expect(thread.locator('.stage-doctor__scope')).toContainText('Elira')

  // Accept the lore entry → world.lore.put with the proposed payload.
  await lore.locator('.stage-doctor__accept').click()
  await expect(lore).toContainText('✓ applied')
  expect(puts[0]).toMatchObject({ entry: { name: 'The wage ladder', keys: ['wage'], content: '8s, then 10 after trial.' } })

  // The promotion is a prefilled, editable seed — tweak a field, then accept:
  // cards.import carries the EDITED card, and cast.add stages the new id.
  const promo = page.locator('.stage-doctor__item', { hasText: 'Marrow' })
  const descField = promo.locator('.stage-doctor__field', { hasText: 'description' }).locator('textarea')
  await descField.fill('The lamplighter who keeps every key in Lowtown.')
  await promo.locator('.stage-doctor__accept').click()
  await expect(promo).toContainText('✓ applied')
  expect(imports).toHaveLength(1)
  const imported = JSON.parse(atob(imports[0].bytes as string))
  expect(imported).toMatchObject({ name: 'Marrow', description: 'The lamplighter who keeps every key in Lowtown.' })
  expect(castAdds[0]).toMatchObject({ name: 'Marrow', ref: 'marrow-9' })

  // Decline the thread with a reason, then revise: the second sessions.doctor
  // call carries every decision — the accepts (kept out of round two) and the
  // decline with its reason.
  await thread.locator('.stage-doctor__decline').click()
  await thread.locator('.stage-doctor__reason input').fill('too early to pin this')
  await thread.locator('.stage-doctor__reason button').click()
  await expect(thread).toContainText('✗ declined')
  await page.locator('.stage-doctor__revise').click()
  await expect(page.locator('.stage-doctor__note')).toHaveText('Revised.')
  expect(doctorCalls).toHaveLength(2)
  const decisions = (doctorCalls[1] as { decisions: Record<string, unknown>[] }).decisions
  expect(decisions).toEqual(
    expect.arrayContaining([
      expect.objectContaining({ proposal_id: 'p1', accepted: true }),
      expect.objectContaining({ proposal_id: 'p2', accepted: false, reason: 'too early to pin this' }),
      expect.objectContaining({ proposal_id: 'p3', accepted: true }),
    ]),
  )
})