import { test, expect } from '@playwright/test'
import { installStageBackend } from './support'

// The doctor negotiation (S7.3): apply some proposals, decline others WITH a
// reason, then "Save & ask again" — which persists the staged edits (so the
// doctor re-reads the improved card) and re-runs cards.doctor with the decisions
// so it revises. Driven against a mock that captures the save and the decisions.
test('stage: decline-with-reason feeds back into a doctor revise', async ({ page }) => {
  let doctorCalls = 0
  let edited = false
  let secondDecisions: unknown = null

  await installStageBackend(page, {
    respond: (method, params) => {
      if (method === 'sessions.list') return { sessions: [] }
      if (method === 'cards.get')
        return {
          id: 'card-1',
          name: 'Ivy',
          greetings: 1,
          raw: { spec: 'chara_card_v2', spec_version: '2.0', data: { name: 'Ivy', first_mes: 'Hey {{user)}.', personality: '' } },
        }
      if (method === 'cards.lint') return { findings: [] }
      if (method === 'cards.edit') {
        edited = true
        return { id: 'card-1', name: 'Ivy', greetings: 1, raw: {} }
      }
      if (method === 'cards.doctor') {
        doctorCalls++
        if (doctorCalls === 1)
          return {
            note: 'Two ideas.',
            proposals: [
              { id: 'p1', field: 'first_mes', severity: 'warn', rationale: 'fix macro', before: 'Hey {{user)}.', after: 'Hey {{user}}.' },
              { id: 'p2', field: 'personality', severity: 'suggestion', rationale: 'add a personality', before: '', after: 'Sharp but warm.' },
            ],
          }
        // Second call: the revise. Capture the decisions it threaded back.
        secondDecisions = (params as { decisions?: unknown }).decisions
        return { note: 'Revised per your note.', proposals: [{ id: 'p3', field: 'scenario', severity: 'suggestion', rationale: 'set a scene', before: '', after: 'A quiet flower shop.' }] }
      }
      return undefined
    },
  })

  await page.goto('/stage.html')
  await expect(page.locator('.stage-card')).toHaveCount(1)
  await page.locator('.stage-card__more').first().click()
  await page.locator('.stage-cardsheet__edit').click()
  await expect(page.locator('.stage-cardeditor')).toBeVisible()

  await page.locator('.stage-doctor__run').click()
  await expect(page.locator('.stage-doctor__item')).toHaveCount(2)

  const macro = page.locator('.stage-doctor__item', { hasText: 'First message' })
  const persona = page.locator('.stage-doctor__item', { hasText: 'Personality' })

  // Apply the macro fix.
  await macro.locator('.stage-doctor__apply').click()
  await expect(macro.locator('.stage-doctor__verdict--applied')).toBeVisible()

  // Decline the personality suggestion with a reason.
  await persona.locator('.stage-doctor__declinebtn').click()
  await persona.locator('.stage-doctor__reason').fill('I want personality to stay empty for now.')
  await persona.locator('.stage-doctor__apply', { hasText: 'Confirm decline' }).click()
  await expect(persona.locator('.stage-doctor__verdict--declined')).toContainText('stay empty')

  // "Save & ask again" persists the edit and re-runs with the decisions.
  await page.locator('.stage-doctor__revise').click()

  // The revised proposal renders, and only after a save happened.
  await expect(page.locator('.stage-doctor__item')).toHaveCount(1)
  await expect(page.locator('.stage-doctor__item')).toContainText('Scenario')
  expect(edited).toBe(true)
  expect(doctorCalls).toBe(2)
  // The decisions carried the accept and the decline-with-reason.
  expect(secondDecisions).toEqual([
    { proposal_id: 'p1', field: 'first_mes', rationale: 'fix macro', accepted: true },
    { proposal_id: 'p2', field: 'personality', rationale: 'add a personality', accepted: false, reason: 'I want personality to stay empty for now.' },
  ])
  if (process.env.NEGO_SHOT) await page.screenshot({ path: `${process.env.NEGO_SHOT}.png` })
})
