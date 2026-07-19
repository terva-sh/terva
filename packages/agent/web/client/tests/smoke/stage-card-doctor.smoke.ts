import { test, expect } from '@playwright/test'
import { installMockBackend } from './support'

// Card doctor (S7.2): from the editor, "Ask the doctor" runs the LLM card-craft
// pass (cards.doctor) and renders structured per-field proposals; "Apply" stages
// a proposal's `after` into the editor field (saved with the rest). Driven
// against a mocked doctor response — no real model.
test('stage: run the card doctor and apply a proposal', async ({ page }) => {
  const mock = await installMockBackend(page, {
    respond: (method) => {
      if (method === 'cards.list') return { cards: [{ id: 'card-1', name: 'Ivy', greetings: 1 }] }
      if (method === 'personas.list') return { personas: [] }
      if (method === 'sessions.list') return { sessions: [] }
      if (method === 'cards.get')
        return {
          id: 'card-1',
          name: 'Ivy',
          greetings: 1,
          raw: { spec: 'chara_card_v2', spec_version: '2.0', data: { name: 'Ivy', first_mes: 'Hey {{user)}, welcome in.', personality: '' } },
        }
      if (method === 'cards.lint')
        return { findings: [{ rule: 'malformed-macro', severity: 'warn', field: 'first_mes', message: 'Malformed macro', detail: '{{user)}' }] }
      if (method === 'cards.doctor')
        return {
          note: 'One macro fix and a personality nudge.',
          proposals: [
            {
              id: 'p1',
              field: 'first_mes',
              severity: 'warn',
              rationale: 'Fix the malformed {{user)} macro so it substitutes.',
              before: 'Hey {{user)}, welcome in.',
              after: 'Hey {{user}}, welcome in.',
            },
            {
              id: 'p2',
              field: 'personality',
              severity: 'suggestion',
              rationale: 'The personality is empty; give the model something to hold.',
              before: '',
              after: 'Sharp-tongued but warm underneath.',
            },
          ],
        }
      return undefined
    },
  })

  await page.goto('/stage.html')
  await expect(page.locator('.stage-card')).toHaveCount(1)

  await page.locator('.stage-card__more').first().click()
  await page.locator('.stage-cardsheet__edit').click()
  await expect(page.locator('.stage-cardeditor')).toBeVisible()

  // Ask the doctor → the note and both proposals render.
  await page.locator('.stage-doctor__run').click()
  await expect(page.locator('.stage-doctor__note')).toContainText('One macro fix')
  await expect(page.locator('.stage-doctor__item')).toHaveCount(2)
  // The warning sorts first and shows its before/after.
  const first = page.locator('.stage-doctor__item').first()
  await expect(first.locator('.stage-doctor__sev--warn')).toBeVisible()
  await expect(first.locator('.stage-doctor__after')).toContainText('Hey {{user}}, welcome in.')

  // Apply the macro fix → the first-message field takes the proposed value.
  await first.locator('.stage-doctor__apply').click()
  await expect(first.locator('.stage-doctor__verdict--applied')).toBeVisible()
  await expect(page.locator('.stage-editfield', { hasText: 'First message' }).locator('textarea')).toHaveValue('Hey {{user}}, welcome in.')
  if (process.env.DOCTOR_SHOT) await page.screenshot({ path: `${process.env.DOCTOR_SHOT}.png` })
})
