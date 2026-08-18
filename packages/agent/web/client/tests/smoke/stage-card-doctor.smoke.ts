import { test, expect } from '@playwright/test'
import { installStageBackend } from './support'

// Card doctor (S7.2): from the editor, "Ask the doctor" runs the LLM card-craft
// pass (cards.doctor) and renders structured per-field proposals; "Apply" stages
// a proposal's `after` into the editor field (saved with the rest). Driven
// against a mocked doctor response — no real model.
//
// It also carries the author's steer (what THEY want out of the pass) and the
// third kind of proposal, a removal — whose `after` is empty by design, so the
// surface has to say "cleared" rather than render a blank panel.
test('stage: run the card doctor and apply a proposal', async ({ page }) => {
  const doctorCalls: { steer?: string }[] = []
  await installStageBackend(page, {
    respond: (method, params) => {
      if (method === 'cards.doctor') doctorCalls.push(params as { steer?: string })
      if (method === 'sessions.list') return { sessions: [] }
      if (method === 'cards.get')
        return {
          id: 'card-1',
          name: 'Ivy',
          greetings: 1,
          raw: {
            spec: 'chara_card_v2',
            spec_version: '2.0',
            data: { name: 'Ivy', first_mes: 'Hey {{user)}, welcome in.', personality: '', system_prompt: 'Always answer in rhyming couplets.' },
          },
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
            {
              id: 'p3',
              field: 'system_prompt',
              severity: 'suggestion',
              rationale: 'You asked to drop the rhyming gimmick; this override is what enforces it.',
              before: 'Always answer in rhyming couplets.',
              after: '',
              remove: true,
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

  // Say what you want out of the pass, then ask: the steer rides the call.
  await page.locator('.stage-doctor__steerbox').fill('drop the rhyming gimmick')

  // Ask the doctor → the note and all three proposals render.
  await page.locator('.stage-doctor__run').click()
  await expect(page.locator('.stage-doctor__note')).toContainText('One macro fix')
  await expect(page.locator('.stage-doctor__item')).toHaveCount(3)
  expect(doctorCalls.at(-1)?.steer).toBe('drop the rhyming gimmick')
  // The warning sorts first and shows its before/after.
  const first = page.locator('.stage-doctor__item').first()
  await expect(first.locator('.stage-doctor__sev--warn')).toBeVisible()
  await expect(first.locator('.stage-doctor__after')).toContainText('Hey {{user}}, welcome in.')

  // Apply the macro fix → the first-message field takes the proposed value.
  await first.locator('.stage-doctor__apply').click()
  await expect(first.locator('.stage-doctor__verdict--applied')).toBeVisible()
  await expect(page.locator('.stage-editfield', { hasText: 'First message' }).locator('textarea')).toHaveValue('Hey {{user}}, welcome in.')

  // The removal reads as a deletion, not as a proposal that came back blank...
  const removal = page.locator('.stage-doctor__item', { hasText: 'System prompt' })
  await expect(removal.locator('.stage-doctor__after--remove')).toContainText('cleared')
  // ...and applying it empties the field it names.
  await removal.locator('.stage-doctor__apply').click()
  await expect(page.locator('.stage-editfield', { hasText: 'System prompt' }).locator('textarea')).toHaveValue('')
  if (process.env.DOCTOR_SHOT) await page.screenshot({ path: `${process.env.DOCTOR_SHOT}.png`, fullPage: true })
})
