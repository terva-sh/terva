import { test, expect } from '@playwright/test'
import { SMOKE_SESSION, installStageBackend, stubMedia } from './support'

// The doctor's small hands (SD2/SD3): a message's edit actions carry
// "Keep as lore" (any message → a focused sessions.doctor run) and, on an
// attributed line whose actor is not on stage, "Promote to cast" (a promote
// run). Both open the doctor overlay; accepts apply through the same verbs
// as the full sweep. Zero backend.
test('stage: keep-as-lore and promote-to-cast run narrowed doctor asks', async ({ page }) => {
  await stubMedia(page)

  const SESSION = { id: SMOKE_SESSION, title: 'The Lowtown Job', experience: 'chat', card: 'elira-1', cast: {}, world_lore: [] }
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
        const p = params as Record<string, unknown>
        doctorCalls.push(p)
        if (p.focus !== undefined) {
          return {
            proposals: [
              { id: 'p1', kind: 'lore_entry', rationale: 'the marked moment', name: 'The stew rule', content: 'The pot never empties.', keys: ['stew'] },
            ],
          }
        }
        return {
          proposals: [
            { id: 'p1', kind: 'cast_promotion', rationale: 'their played lines', character: 'Marrow', description: 'The lamplighter.', personality: 'grave', first_mes: '*He lifts the pole.*' },
          ],
        }
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
    {
      type: 'snapshot',
      snapshot: {
        session: SESSION,
        epoch: 1,
        base: 0,
        total: 2,
        messages: [
          { role: 'user', content: [{ type: 'text', text: 'The pot never empties — house rule.' }] },
          // A directed walk-on line: the actor is NOT on the roster, so this
          // message's actions carry the promote button.
          { role: 'assistant', content: [{ type: 'text', text: '*Marrow lifts the pole.*' }], directed: true, actor: 'Marrow' },
        ],
        busy: false,
      },
    },
    SMOKE_SESSION,
  )

  // SD2 — mark the user's message as lore: focused run, accept, world.lore.put.
  await page.locator('.stage-row--user .stage-msgedit').click()
  await page.locator('.stage-edit__lore').click()
  const sheet = page.locator('.stage-doctor-sheet')
  await expect(sheet).toBeVisible()
  await expect(sheet.locator('h3')).toContainText('Keep this moment as lore')
  await expect(sheet.locator('.stage-doctor__item')).toHaveCount(1)
  expect(doctorCalls[0]).toMatchObject({ focus: 0 })
  await sheet.locator('.stage-doctor__accept').click()
  await expect(sheet.locator('.stage-doctor__item')).toContainText('✓ applied')
  expect(puts[0]).toMatchObject({ entry: { name: 'The stew rule', keys: ['stew'], content: 'The pot never empties.' } })
  await sheet.locator('.stage-drawer__close').click()

  // SD3 — promote the walk-on: the directed line's actions carry the button,
  // the run is narrowed to the actor, the prefilled seed applies as
  // cards.import + cast.add.
  await page.locator('.stage-row--directed .stage-msgedit').click()
  await expect(page.locator('.stage-edit__promote')).toBeVisible()
  await page.locator('.stage-edit__promote').click()
  await expect(sheet).toBeVisible()
  await expect(sheet.locator('h3')).toContainText('Promote Marrow to the cast')
  await expect(sheet.locator('.stage-doctor__field')).toHaveCount(4)
  expect(doctorCalls[1]).toMatchObject({ promote: 'Marrow' })
  await sheet.locator('.stage-doctor__accept').click()
  await expect(sheet.locator('.stage-doctor__item')).toContainText('✓ applied')
  expect(imports).toHaveLength(1)
  const imported = JSON.parse(atob(imports[0].bytes as string))
  expect(imported).toMatchObject({ name: 'Marrow', description: 'The lamplighter.' })
  expect(castAdds[0]).toMatchObject({ name: 'Marrow', ref: 'marrow-9' })
})